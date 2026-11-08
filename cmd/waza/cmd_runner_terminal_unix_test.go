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
			child.WaitDelay = time.Second
			child.Dir = t.TempDir()
			input, err := child.StdinPipe()
			require.NoError(t, err)
			defer input.Close() //nolint:errcheck
			inputFile, ok := input.(*os.File)
			require.True(t, ok, "real process stdin must support a native write deadline")
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
			requireRunnerStartupInput(t, ctx, cancel, child, inputFile, &stdout, &stderr)
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
	child.WaitDelay = time.Second
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
	requireRunnerStartupInput(t, ctx, cancel, child, producer, &stdout, &stderr)
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "active input must not change its peer's status flags")

	require.NoError(t, child.Process.Signal(syscall.SIGTERM))
	exit, ok := errors.AsType[*exec.ExitError](child.Wait())
	require.True(t, ok, "%s", stderr.String())
	require.Equal(t, 3, exit.ExitCode(), "stderr: %s; stdout: %s", stderr.String(), stdout.String())
	require.Zero(t, runnerFDFlags(t, peer)&unix.O_NONBLOCK, "cancellation cleanup must not restore or mutate peer flags")
	require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
}

func writeRunnerStartupInput(ctx context.Context, writer *os.File, data []byte) (int, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, errors.New("runner startup input requires a deadline")
	}
	if err := writer.SetWriteDeadline(deadline); err != nil {
		return 0, fmt.Errorf("bound runner startup input: %w", err)
	}
	return writer.Write(data)
}

func requireRunnerStartupInput(t *testing.T, ctx context.Context, cancel context.CancelFunc, child *exec.Cmd, input *os.File, stdout, stderr *bytes.Buffer) {
	t.Helper()
	payload := append(bytes.Repeat([]byte(" "), 2<<20), '{')
	n, err := writeRunnerStartupInput(ctx, input, payload)
	if err != nil {
		contextErr := ctx.Err()
		cancel()
		waitErr := child.Wait()
		t.Fatalf("runner startup input: wrote %d/%d bytes: %v; context before cleanup: %v; joined Wait: %v; state: %v; stdout: %q; stderr: %q",
			n, len(payload), err, contextErr, waitErr, child.ProcessState, stdout.String(), stderr.String())
	}
	require.Equal(t, len(payload), n, "readiness requires the complete oversized input write")
}

func TestRunnerStartupInputWriteBound(t *testing.T) {
	t.Run("retained readers cannot outlive write deadline", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		t.Cleanup(func() { require.NoError(t, writer.Close()) })
		peer := duplicateRunnerFD(t, reader)
		t.Cleanup(func() { require.NoError(t, peer.Close()) })
		before := runnerFDFlags(t, peer)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		payload := bytes.Repeat([]byte("x"), 2<<20)
		n, err := writeRunnerStartupInput(ctx, writer, payload)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		require.Positive(t, n, "the actual native pipe must receive a partial write")
		require.Less(t, n, len(payload), "retained readers must not turn incomplete startup into readiness")
		require.Equal(t, before, runnerFDFlags(t, peer), "write deadlines must not mutate inherited reader flags")
		require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))
		got := make([]byte, n)
		_, err = io.ReadFull(reader, got)
		require.NoError(t, err)
		require.Equal(t, payload[:n], got, "deadline failure must preserve the actual accepted bytes")
	})
	t.Run("draining reader completes", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		t.Cleanup(func() { require.NoError(t, writer.Close()) })
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		payload := bytes.Repeat([]byte("x"), 2<<20)
		require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))
		type readResult struct {
			data []byte
			err  error
		}
		done := make(chan readResult, 1)
		go func() {
			data := make([]byte, len(payload))
			_, err := io.ReadFull(reader, data)
			done <- readResult{data, err}
		}()
		n, err := writeRunnerStartupInput(ctx, writer, payload)
		result := <-done
		require.NoError(t, err)
		require.Equal(t, len(payload), n)
		require.NoError(t, result.err)
		require.Equal(t, payload, result.data)
	})
	t.Run("closed reader preserves broken pipe", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		t.Cleanup(func() { require.NoError(t, writer.Close()) })
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		n, err := writeRunnerStartupInput(ctx, writer, []byte("x"))
		require.ErrorIs(t, err, syscall.EPIPE)
		require.Zero(t, n)
	})
	t.Run("closed writer reports deadline setup failure", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, reader.Close()) })
		require.NoError(t, writer.Close())
		wantErr := writer.SetWriteDeadline(time.Now())
		require.Error(t, wantErr)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		n, err := writeRunnerStartupInput(ctx, writer, []byte("x"))
		require.ErrorIs(t, err, wantErr, "retain the native deadline setup error")
		require.ErrorContains(t, err, "bound runner startup input")
		require.Zero(t, n)
	})
	t.Run("missing deadline is rejected", func(t *testing.T) {
		n, err := writeRunnerStartupInput(t.Context(), nil, []byte("x"))
		require.ErrorContains(t, err, "requires a deadline")
		require.Zero(t, n)
	})
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
	raw, err := file.SyscallConn()
	require.NoError(t, err)
	var flags int
	var flagErr error
	err = raw.Control(func(fd uintptr) {
		flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
	})
	require.NoError(t, err)
	require.NoError(t, flagErr)
	return flags
}

func TestRunnerFDFlagsPreservesPipeFlags(t *testing.T) {
	for _, control := range []struct {
		name        string
		readEnd     bool
		nonblocking bool
	}{
		{name: "reader/blocking", readEnd: true},
		{name: "reader/nonblocking", readEnd: true, nonblocking: true},
		{name: "writer/blocking"},
		{name: "writer/nonblocking", nonblocking: true},
	} {
		t.Run(control.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reader.Close()) })
			t.Cleanup(func() { require.NoError(t, writer.Close()) })
			file := writer
			if control.readEnd {
				file = reader
			}
			peer := duplicateRunnerFD(t, file)
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			raw, err := file.SyscallConn()
			require.NoError(t, err)
			peerRaw, err := peer.SyscallConn()
			require.NoError(t, err)
			var setErr error
			require.NoError(t, raw.Control(func(fd uintptr) {
				setErr = unix.SetNonblock(int(fd), control.nonblocking)
			}))
			require.NoError(t, setErr)

			// Snapshot kernel flags without invoking the getter under test.
			readFlags := func(conn syscall.RawConn) int {
				var flags int
				var flagErr error
				require.NoError(t, conn.Control(func(fd uintptr) {
					flags, flagErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
				}))
				require.NoError(t, flagErr)
				return flags
			}
			before := readFlags(raw)
			require.Equal(t, control.nonblocking, before&unix.O_NONBLOCK != 0)
			require.Equal(t, before, readFlags(peerRaw))
			for range 3 {
				flags := runnerFDFlags(t, file)
				require.Equal(t, before, readFlags(raw), "observing the Go-owned handle must preserve its flags")
				require.Equal(t, before, readFlags(peerRaw), "observing the Go-owned handle must preserve shared peer flags")
				require.Equal(t, before, flags)
				flags = runnerFDFlags(t, peer)
				require.Equal(t, before, readFlags(raw), "observing the peer must preserve the Go-owned handle's flags")
				require.Equal(t, before, readFlags(peerRaw))
				require.Equal(t, before, flags)
			}
		})
	}
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
