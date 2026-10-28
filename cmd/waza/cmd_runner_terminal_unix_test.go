//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerTerminalRealProcessSignalsWithOpenStdin(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, signal := range []os.Signal{syscall.SIGTERM, os.Interrupt, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "runner", "run")
			child.Dir = t.TempDir()
			input, err := child.StdinPipe()
			require.NoError(t, err)
			defer input.Close() //nolint:errcheck
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			require.NoError(t, child.Start())
			t.Cleanup(func() {
				if child.ProcessState == nil {
					require.NoError(t, child.Process.Kill())
					_ = child.Wait()
				}
			})
			// More than the native pipe capacity, below request size bounds:
			// completed write proves the real command has entered its read.
			_, err = input.Write(append(bytes.Repeat([]byte(" "), 2<<20), '{'))
			require.NoError(t, err)
			require.NoError(t, child.Process.Signal(signal))
			err = child.Wait()
			exit, ok := errors.AsType[*exec.ExitError](err)
			require.True(t, ok, "%v: %s", err, stderr.String())
			if signal == syscall.SIGKILL {
				require.Equal(t, -1, exit.ExitCode())
				require.Empty(t, stdout.String(), "uncatchable termination cannot produce a terminal result")
				return
			}
			require.Equal(t, 3, exit.ExitCode(), "stderr: %s; stdout: %s", stderr.String(), stdout.String())
			require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
			envelope, err := runnerterminal.Validate(stdout.Bytes(), nil)
			require.NoError(t, err)
			require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
			require.Contains(t, stdout.String(), `"failure_boundary":"input"`)
			require.NotContains(t, stdout.String(), `"identity"`)
			require.Contains(t, stderr.String(), "runner run: "+string(runnerterminal.Canceled))
		})
	}
}

func TestRunnerTerminalRealProcessInputPreservesInheritedPeerFlags(t *testing.T) {
	binary := buildRecoveryCLI(t)
	reader, producer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()   //nolint:errcheck
	defer producer.Close() //nolint:errcheck
	peer := duplicateRunnerFD(t, reader)
	defer peer.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "runner", "run")
	child.Dir = t.TempDir()
	child.Stdin = reader
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		if child.ProcessState == nil {
			require.NoError(t, child.Process.Kill())
			_ = child.Wait()
		}
	})

	// A completed oversized write proves the child is blocked in its inherited
	// stdin read. The peer shares that input's open file description.
	_, err = producer.Write(append(bytes.Repeat([]byte(" "), 2<<20), '{'))
	require.NoError(t, err)
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "active input must not change its peer's status flags")

	require.NoError(t, child.Process.Signal(syscall.SIGTERM))
	exit, ok := errors.AsType[*exec.ExitError](child.Wait())
	require.True(t, ok, "%s", stderr.String())
	require.Equal(t, 3, exit.ExitCode(), "stderr: %s; stdout: %s", stderr.String(), stdout.String())
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "cancellation cleanup must not restore or mutate peer flags")
	require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
}

func TestRunnerTerminalRealProcessOutputFailure(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, control := range []string{"broken", "blocked"} {
		t.Run(control, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close() //nolint:errcheck
			defer writer.Close() //nolint:errcheck
			var filled []byte
			if control == "broken" {
				require.NoError(t, reader.Close())
			} else {
				fd := int(writer.Fd())
				require.NoError(t, unix.SetNonblock(fd, true))
				for {
					n, writeErr := unix.Write(fd, bytes.Repeat([]byte("x"), 4096))
					if writeErr != nil {
						require.ErrorIs(t, writeErr, unix.EAGAIN)
						break
					}
					filled = append(filled, bytes.Repeat([]byte("x"), n)...)
				}
				require.NotEmpty(t, filled, "the native output pipe must really be full")
				require.NoError(t, unix.SetNonblock(fd, false))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "runner", "run")
			child.Dir = t.TempDir()
			child.Stdin, child.Stdout = bytes.NewReader(request), writer
			var stderr bytes.Buffer
			child.Stderr = &stderr
			require.NoError(t, child.Start())
			require.NoError(t, writer.Close())
			exit, ok := errors.AsType[*exec.ExitError](child.Wait())
			require.True(t, ok, "%s", stderr.String())
			require.Equal(t, 2, exit.ExitCode(), "%s", stderr.String())
			require.NoError(t, ctx.Err(), "output failure must return before forced termination")
			if control == "blocked" {
				output, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.Equal(t, filled, output, "no terminal fallback may be appended after failed delivery")
				require.Contains(t, stderr.String(), "i/o timeout")
			} else {
				require.Contains(t, stderr.String(), "broken pipe")
			}
		})
	}
}

func TestRunnerTerminalRealProcessOutputPreservesInheritedPeerFlags(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close() //nolint:errcheck
	defer writer.Close() //nolint:errcheck
	peer := duplicateRunnerFD(t, writer)
	defer peer.Close() //nolint:errcheck
	fillRunnerPipe(t, peer)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "runner", "run")
	child.Dir = t.TempDir()
	child.Stdin, child.Stdout = bytes.NewReader(request), writer
	var stderr bytes.Buffer
	child.Stderr = &stderr
	require.NoError(t, child.Start())
	require.NoError(t, writer.Close())
	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()

	// The fault-controlled full pipe keeps the sole terminal write pending.
	// Its liveness is the barrier before inspecting the shared peer descriptor.
	select {
	case err := <-wait:
		require.FailNowf(t, "terminal output did not block", "wait error: %v; stderr: %s", err, stderr.String())
	case <-time.After(250 * time.Millisecond):
	}
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "active output must not change its peer's status flags")

	// A peer may legitimately change its own status flags while this invocation
	// is blocked. Cleanup must not restore a cached stale value over that change.
	require.NoError(t, unix.SetNonblock(int(peer.Fd()), true))
	defer unix.SetNonblock(int(peer.Fd()), false) //nolint:errcheck
	exit, ok := errors.AsType[*exec.ExitError](<-wait)
	require.True(t, ok, "%s", stderr.String())
	require.Equal(t, 2, exit.ExitCode(), "%s", stderr.String())
	require.NoError(t, ctx.Err(), "output failure must return before forced termination")
	require.NotZero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "cleanup must preserve the peer's flag transition")
	require.NoError(t, peer.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NotEmpty(t, output, "the native output pipe must really have been full")
}

func duplicateRunnerFD(t *testing.T, file *os.File) *os.File {
	t.Helper()
	fd, err := unix.Dup(int(file.Fd()))
	require.NoError(t, err)
	peer := os.NewFile(uintptr(fd), "runner-peer")
	require.NotNil(t, peer)
	return peer
}

func runnerFDFlags(t *testing.T, file *os.File) int {
	t.Helper()
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	require.NoError(t, err)
	return flags
}

func fillRunnerPipe(t *testing.T, writer *os.File) {
	t.Helper()
	fd := int(writer.Fd())
	require.NoError(t, unix.SetNonblock(fd, true))
	defer func() {
		require.NoError(t, unix.SetNonblock(fd, false))
	}()
	for {
		_, err := unix.Write(fd, bytes.Repeat([]byte("x"), 4096))
		if errors.Is(err, unix.EAGAIN) {
			return
		}
		require.NoError(t, err)
	}
}
