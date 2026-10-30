//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
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
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(raw)
	require.NoError(t, err)
	// HTML-escaped, bounded grader names make the real terminal record larger
	// than the pipe. A partial record is the output-readiness rendezvous.
	request.Input.Eval += "graders:\n"
	for i := range runnerterminal.MaxGraders - 1 {
		request.Input.Eval += fmt.Sprintf("  - name: '%03d%s'\n    type: text\n    config:\n      contains: [hello]\n",
			i, strings.Repeat("<", runnerterminal.MaxIdentifierBytes-3))
	}
	data, err := json.Marshal(request)
	require.NoError(t, err)
	envelope, binding := runnerunit.NewService(nil).RunTerminal(t.Context(), data)
	require.Equal(t, runnerterminal.Success, envelope.Outcome)
	require.Len(t, envelope.Observation.Validations, runnerterminal.MaxGraders)
	expected, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close() //nolint:errcheck
	defer writer.Close() //nolint:errcheck
	peer := duplicateRunnerFD(t, writer)
	defer peer.Close() //nolint:errcheck
	filled := fillRunnerPipe(t, peer)
	require.Greater(t, len(expected), 2*len(filled), "the actual record must exceed the native pipe capacity")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "runner", "run")
	child.Dir = t.TempDir()
	child.Stdin, child.Stdout = bytes.NewReader(data), writer
	var stderr bytes.Buffer
	child.Stderr = &stderr
	require.NoError(t, child.Start())
	require.NoError(t, writer.Close())
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = child.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.NoError(t, reader.SetReadDeadline(deadline))
	drained := make([]byte, len(filled))
	_, err = io.ReadFull(reader, drained)
	require.NoError(t, err)
	require.Equal(t, filled, drained)
	prefix := make([]byte, 1)
	_, err = io.ReadFull(reader, prefix)
	require.NoError(t, err, "readiness requires bytes from the real terminal write, not child liveness")
	require.Equal(t, expected[:1], prefix)

	// The real write has started and its oversized remainder cannot fit.
	// Slow process startup cannot reach the flag assertion prematurely.
	select {
	case <-done:
		require.FailNowf(t, "terminal output did not block", "wait error: %v; stderr: %s", waitErr, stderr.String())
	default:
	}
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "active output must not change its peer's status flags")

	// A peer may legitimately change its own status flags while this invocation
	// is blocked. Cleanup must not restore a cached stale value over that change.
	require.NoError(t, unix.SetNonblock(int(peer.Fd()), true))
	defer unix.SetNonblock(int(peer.Fd()), false) //nolint:errcheck
	<-done
	exit, ok := errors.AsType[*exec.ExitError](waitErr)
	require.True(t, ok, "%s", stderr.String())
	require.Equal(t, 2, exit.ExitCode(), "%s", stderr.String())
	require.NoError(t, ctx.Err(), "output failure must return before forced termination")
	require.NotZero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "cleanup must preserve the peer's flag transition")
	require.NoError(t, peer.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	output = append(prefix, output...)
	require.GreaterOrEqual(t, len(output), len(filled), "the real terminal write must fill the pipe")
	require.Less(t, len(output), len(expected), "the real terminal write must remain incomplete")
	require.Equal(t, expected[:len(output)], output, "no terminal fallback may follow failed delivery")
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

func fillRunnerPipe(t *testing.T, writer *os.File) []byte {
	t.Helper()
	fd := int(writer.Fd())
	require.NoError(t, unix.SetNonblock(fd, true))
	defer func() {
		require.NoError(t, unix.SetNonblock(fd, false))
	}()
	var filled []byte
	for {
		n, err := unix.Write(fd, bytes.Repeat([]byte("x"), 4096))
		if n > 0 {
			filled = append(filled, bytes.Repeat([]byte("x"), n)...)
		}
		if errors.Is(err, unix.EAGAIN) {
			require.NotEmpty(t, filled, "the native output pipe must really be full")
			return filled
		}
		require.NoError(t, err)
	}
}
