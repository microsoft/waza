package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// Err is first consulted by the reader after registering its AfterFunc.
// Closing before canceling makes the callback's native failure deterministic.
type runnerWindowsCloseBeforeCancel struct {
	context.Context
	once   sync.Once
	before func()
	cancel context.CancelFunc
}

func (c *runnerWindowsCloseBeforeCancel) Err() error {
	c.once.Do(func() {
		c.before()
		c.cancel()
	})
	return c.Context.Err()
}

func newRunnerWindowsDeadlineInput(t *testing.T) *os.File {
	t.Helper()
	name, err := windows.UTF16PtrFromString(`\\.\pipe\waza-runner-input-` + rand.Text())
	require.NoError(t, err)
	server, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|
		windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	require.NoError(t, err)
	input := os.NewFile(uintptr(server), "runner-deadline-input")
	t.Cleanup(func() {
		if err := input.Close(); !errors.Is(err, os.ErrClosed) {
			require.NoError(t, err)
		}
	})
	client, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(client)) })
	var overlapped windows.Overlapped
	require.ErrorIs(t, windows.ConnectNamedPipe(server, &overlapped), windows.ERROR_PIPE_CONNECTED,
		"client connected before the server's nonpending connect check")
	require.NoError(t, input.SetReadDeadline(time.Time{}), "overlapped native input must really support deadlines")
	return input
}

func TestRunnerTerminalWindowsInputInterruptionErrors(t *testing.T) {
	for _, control := range []string{"deadline", "close"} {
		t.Run(control, func(t *testing.T) {
			var input *os.File
			if control == "deadline" {
				input = newRunnerWindowsDeadlineInput(t)
			} else {
				var read, write windows.Handle
				require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
				input = os.NewFile(uintptr(read), "runner-no-deadline-input")
				t.Cleanup(func() { require.NoError(t, windows.CloseHandle(write)) })
				t.Cleanup(func() {
					if err := input.Close(); !errors.Is(err, os.ErrClosed) {
						require.NoError(t, err)
					}
				})
				require.ErrorIs(t, input.SetReadDeadline(time.Time{}), os.ErrNoDeadline)
			}
			originalInput, originalDiagnostic := os.Stdin, os.Stderr
			diagnostic, err := os.Create(t.TempDir() + "/diagnostic.txt")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, diagnostic.Close()) })
			os.Stdin, os.Stderr = input, diagnostic
			t.Cleanup(func() { os.Stdin, os.Stderr = originalInput, originalDiagnostic })
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			closed := false
			interruptionCause := os.ErrClosed
			ctx := &runnerWindowsCloseBeforeCancel{Context: parent, cancel: cancel, before: func() {
				require.NoError(t, input.Close())
				closed = true
				if control == "deadline" {
					interruptionCause = input.SetReadDeadline(time.Now())
					require.Error(t, interruptionCause, "capture the actual closed pollable-handle error")
				}
			}}
			data, err := readRunnerInput(ctx, input)
			require.True(t, closed, "native close must occur after initial deadline admission, before cancellation")
			require.Empty(t, data)
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, err, interruptionCause, "join the native callback failure before returning")
			if control == "deadline" {
				require.ErrorContains(t, err, "interrupting runner input:")
				require.ErrorContains(t, err, "resetting runner input deadline:")
			} else {
				require.ErrorContains(t, err, "closing interrupted runner stdin:")
			}
			envelope := runnerterminal.Failure("input", err)
			require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
			require.Equal(t, 3, envelope.ExitCode, "joined I/O failure must not replace the selected cancellation class")
			_, err = diagnostic.Seek(0, io.SeekStart)
			require.NoError(t, err)
			output, err := io.ReadAll(diagnostic)
			require.NoError(t, err)
			require.Empty(t, output, "interruption callbacks must return errors, not synchronously write inherited stderr")
		})
	}
}

const runnerWindowsInputErrorHelper = "WAZA_RUNNER_WINDOWS_INPUT_ERROR_HELPER"

// The helper's only fault injection is closing its native stdin before running
// the real runner command with an already-canceled context. No controller,
// console signal, model, or production command/exit implementation is replaced.
func TestRunnerTerminalWindowsInputErrorProcessHelper(t *testing.T) {
	if os.Getenv(runnerWindowsInputErrorHelper) != "1" {
		return
	}
	if err := os.Stdin.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	command := newRunnerCommand()
	command.SetArgs([]string{"run"})
	command.SetIn(os.Stdin)
	command.SetOut(os.Stdout)
	diagnostics := &processDiagnosticWriter{output: processStderr{}}
	command.SetErr(diagnostics)
	err := command.ExecuteContext(ctx)
	exit, ok := errors.AsType[*ExitCodeError](err)
	if !ok || exit.Code != 3 {
		t.Fatalf("expected selected cancellation exit 3, got %v", err)
	}
	// os.Exit bypasses TestMain's defer; this canceled command created nothing
	// in its invocation-owned isolated temp directory. Remove only that empty
	// directory, never recursively delete potentially competing contents.
	if err := os.Remove(os.Getenv("TMPDIR")); err != nil {
		t.Fatal(err)
	}
	// Match the existing process diagnostic/exit path, including its shared
	// bounded writer. This is a compiled command helper, not the ordinary CLI.
	if _, diagnosticErr := fmt.Fprintln(diagnostics, exit.Err); diagnosticErr != nil {
		os.Exit(exit.Code)
	}
	os.Exit(exit.Code)
}

func TestRunnerTerminalWindowsInputErrorFullStderrProcess(t *testing.T) {
	var read, write windows.Handle
	require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
	reader := os.NewFile(uintptr(read), "runner-input-error-stderr-reader")
	writer := os.NewFile(uintptr(write), "runner-input-error-stderr-writer")
	writerClosed := false
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	t.Cleanup(func() {
		if !writerClosed {
			require.NoError(t, writer.Close())
		}
	})
	var peerHandle windows.Handle
	require.NoError(t, windows.DuplicateHandle(windows.CurrentProcess(), write,
		windows.CurrentProcess(), &peerHandle, 0, false, windows.DUPLICATE_SAME_ACCESS))
	peer := os.NewFile(uintptr(peerHandle), "runner-input-error-stderr-peer")
	peerClosed := false
	t.Cleanup(func() {
		if !peerClosed {
			require.NoError(t, peer.Close())
		}
	})
	filled := fillRunnerWindowsStdout(t, read, write)
	mode := uint32(windows.PIPE_WAIT)
	require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
	require.Equal(t, uint32(windows.PIPE_WAIT), runnerWindowsPipeState(t, peerHandle)&windows.PIPE_NOWAIT)
	require.ErrorIs(t, writer.SetWriteDeadline(time.Now()), os.ErrNoDeadline)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerTerminalWindowsInputErrorProcessHelper$")
	child.Dir = t.TempDir()
	child.Env = append(os.Environ(), runnerWindowsInputErrorHelper+"=1")
	child.Stdin = bytes.NewReader(nil)
	child.Stderr = writer
	var stdout bytes.Buffer
	child.Stdout = &stdout
	require.NoError(t, child.Start())
	start := time.Now()
	waitErr := child.Wait()
	require.NoError(t, ctx.Err(), "watchdog termination is not natural input-error exit: %v", waitErr)
	require.Less(t, time.Since(start), 5*time.Second, "callback must not wait on the full inherited stderr pipe")
	exit, ok := errors.AsType[*exec.ExitError](waitErr)
	require.True(t, ok, "%v: %s", waitErr, stdout.String())
	require.Equal(t, 3, exit.ExitCode(), "%s", stdout.String())
	envelope, err := runnerterminal.Validate(stdout.Bytes(), nil)
	require.NoError(t, err, "%s", stdout.String())
	require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
	require.Equal(t, 3, envelope.ExitCode)
	require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
	require.Len(t, envelope.Diagnostics, 1)
	require.Contains(t, envelope.Diagnostics[0].Message, "closing interrupted runner stdin:")
	require.Contains(t, envelope.Diagnostics[0].Message, "file already closed")
	require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read),
		"no drain until natural Wait; callback must not emit a second synchronous diagnostic")
	require.Equal(t, uint32(windows.PIPE_WAIT), runnerWindowsPipeState(t, peerHandle)&windows.PIPE_NOWAIT)
	require.NoError(t, writer.Close())
	writerClosed = true
	require.NoError(t, peer.Close())
	peerClosed = true
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, filled, output, "full stderr must retain exactly its prefill")
}
