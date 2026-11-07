package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func runnerWindowsPipeState(t *testing.T, handle windows.Handle) uint32 {
	t.Helper()
	var state uint32
	require.NoError(t, windows.GetNamedPipeHandleState(handle, &state, nil, nil, nil, nil, 0))
	return state
}

func runnerWindowsPipeOccupancy(t *testing.T, handle windows.Handle) uint32 {
	t.Helper()
	var available uint32
	peek := windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")
	ok, _, err := peek.Call(uintptr(handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
	require.NotZero(t, ok, "PeekNamedPipe: %v", err)
	return available
}

// Only test-owned handles are switched to NOWAIT, so filling cannot hang.
// The blocked control restores WAIT before starting the production process.
func fillRunnerWindowsStdout(t *testing.T, read, write windows.Handle) []byte {
	t.Helper()
	mode := uint32(windows.PIPE_NOWAIT)
	require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
	require.Equal(t, uint32(windows.PIPE_NOWAIT), runnerWindowsPipeState(t, write)&windows.PIPE_NOWAIT)
	var filled []byte
	chunk := bytes.Repeat([]byte("x"), 4096)
	for {
		var n uint32
		err := windows.WriteFile(write, chunk, &n, nil)
		require.LessOrEqual(t, n, uint32(len(chunk)))
		filled = append(filled, chunk[:n]...)
		if err != nil {
			require.ErrorIs(t, err, windows.ERROR_NO_DATA)
			require.Zero(t, n)
			break
		}
		if n == 0 {
			break
		}
		require.Less(t, len(filled), 1<<20, "anonymous pipe filling must be bounded")
	}
	require.NotEmpty(t, filled)
	require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read))
	var n uint32
	err := windows.WriteFile(write, []byte("!"), &n, nil)
	require.Zero(t, n, "even one more byte must encounter actual backpressure")
	if err != nil {
		require.ErrorIs(t, err, windows.ERROR_NO_DATA)
	}
	return filled
}

func TestRunnerTerminalWindowsInheritedStdout(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.Equal(t, "ready", admitted.Status)
	require.NotNil(t, admitted.Resolved)
	require.NotNil(t, admitted.Identity)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity, GraderIDs: []string{"_output_contains"}}
	for _, control := range []string{"blocked", "nonblocking-full", "broken", "writable"} {
		t.Run(control, func(t *testing.T) {
			var read, write windows.Handle
			require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
			reader := os.NewFile(uintptr(read), "runner-stdout-reader")
			writer := os.NewFile(uintptr(write), "runner-stdout-writer")
			readerClosed, writerClosed := false, false
			t.Cleanup(func() {
				if !writerClosed {
					require.NoError(t, writer.Close())
				}
				if !readerClosed {
					require.NoError(t, reader.Close())
				}
			})
			require.ErrorIs(t, writer.SetWriteDeadline(time.Now()), os.ErrNoDeadline,
				"native inherited synchronous anonymous pipe must lack deadline support")
			var peerHandle windows.Handle
			require.NoError(t, windows.DuplicateHandle(windows.CurrentProcess(), write,
				windows.CurrentProcess(), &peerHandle, 0, false, windows.DUPLICATE_SAME_ACCESS))
			peer := os.NewFile(uintptr(peerHandle), "runner-stdout-retained-peer")
			peerClosed := false
			t.Cleanup(func() {
				if !peerClosed {
					require.NoError(t, peer.Close())
				}
			})
			var filled []byte
			if control == "blocked" || control == "nonblocking-full" {
				filled = fillRunnerWindowsStdout(t, read, write)
				if control == "blocked" {
					mode := uint32(windows.PIPE_WAIT)
					require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
				}
			}
			wantState := uint32(windows.PIPE_WAIT)
			if control == "nonblocking-full" {
				wantState = windows.PIPE_NOWAIT
			}
			require.Equal(t, wantState, runnerWindowsPipeState(t, peerHandle)&windows.PIPE_NOWAIT)
			if control == "broken" {
				require.NoError(t, reader.Close())
				readerClosed = true
			}
			var nativeWriteError *os.PathError
			if control == "nonblocking-full" || control == "broken" {
				n, writeErr := writer.Write([]byte("!"))
				require.Zero(t, n)
				require.ErrorAs(t, writeErr, &nativeWriteError,
					"capture the actual os.File native failure before testing the production child")
			}
			type readResult struct {
				data []byte
				err  error
			}
			drained := make(chan readResult, 1)
			drainJoined := false
			if control == "writable" {
				go func() {
					data, err := io.ReadAll(reader)
					drained <- readResult{data, err}
				}()
				t.Cleanup(func() {
					if !drainJoined {
						// Release the reader before joining even on an assertion failure.
						if !writerClosed {
							require.NoError(t, writer.Close())
							writerClosed = true
						}
						if !peerClosed {
							require.NoError(t, peer.Close())
							peerClosed = true
						}
						select {
						case result := <-drained:
							require.NoError(t, result.err)
						case <-time.After(5 * time.Second):
							t.Error("owned writable-pipe reader survived cleanup")
						}
					}
				})
			}
			// This watchdog is failure cleanup, never a successful exit oracle.
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "runner", "run")
			child.Dir = t.TempDir()
			child.Stdin, child.Stdout = bytes.NewReader(request), writer
			var stderr bytes.Buffer
			child.Stderr = &stderr
			start := time.Now()
			require.NoError(t, child.Start())
			waitErr := child.Wait()
			elapsed := time.Since(start)
			require.NoError(t, ctx.Err(), "watchdog termination is not natural exit: %v; %s", waitErr, stderr.String())
			require.Less(t, elapsed, 12*time.Second, "five-second output budget plus process startup allowance")
			if control != "broken" {
				require.Equal(t, wantState, runnerWindowsPipeState(t, peerHandle)&windows.PIPE_NOWAIT,
					"production must not change its inherited peer's pipe mode")
			}
			if control == "writable" {
				require.NoError(t, waitErr, "%s", stderr.String())
				require.Empty(t, stderr.String())
			} else {
				exit, ok := errors.AsType[*exec.ExitError](waitErr)
				require.True(t, ok, "%v: %s", waitErr, stderr.String())
				require.Equal(t, 2, exit.ExitCode(), "%s", stderr.String())
				require.Contains(t, stderr.String(), "writing terminal output:")
				require.NotContains(t, stderr.String(), "runner run: success")
				if nativeWriteError != nil {
					require.Contains(t, stderr.String(), nativeWriteError.Err.Error(),
						"preserve the actual native error, rather than relabeling it as a timeout")
				}
			}
			switch control {
			case "blocked":
				require.GreaterOrEqual(t, elapsed, 5*time.Second, "a full blocking pipe must exhaust the original budget")
				require.Contains(t, stderr.String(), "i/o timeout")
				require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read),
					"reader and peer remain open, with no drain, through natural child Wait")
			case "nonblocking-full":
				require.NotContains(t, stderr.String(), "i/o timeout", "native immediate write failure must survive")
				require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read))
			case "broken":
				require.NotContains(t, stderr.String(), "i/o timeout", "native broken-pipe error must survive")
			}
			require.NoError(t, writer.Close())
			writerClosed = true
			require.NoError(t, peer.Close())
			peerClosed = true
			if control == "broken" {
				return
			}
			var output []byte
			if control == "writable" {
				select {
				case result := <-drained:
					drainJoined = true
					require.NoError(t, result.err)
					output = result.data
				case <-ctx.Done():
					t.Fatal("writable-pipe reader did not finish after writer cleanup")
				}
				envelope, err := runnerterminal.Validate(output, binding)
				require.NoError(t, err, "%s", output)
				require.Equal(t, runnerterminal.Success, envelope.Outcome)
				require.Zero(t, envelope.ExitCode)
				require.Equal(t, 1, bytes.Count(output, []byte{'\n'}))
			} else {
				output, err = io.ReadAll(reader)
				require.NoError(t, err)
				require.Equal(t, filled, output, "failed delivery must not append a second envelope or fallback")
			}
		})
	}
}

func TestRunnerTerminalWindowsInheritedStdoutSelectedOutcome(t *testing.T) {
	for _, control := range []struct {
		name  string
		cause error
		code  int
	}{
		{"canceled", context.Canceled, 3},
		{"timeout", context.DeadlineExceeded, 4},
		{"infrastructure", errors.New("injected infrastructure failure"), 2},
	} {
		t.Run(control.name, func(t *testing.T) {
			output, err := os.Create(filepath.Join(t.TempDir(), "terminal.json"))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, output.Close()) })
			original := os.Stdout
			os.Stdout = output
			t.Cleanup(func() { os.Stdout = original })
			envelope := runnerterminal.Failure("input", control.cause)
			code, err := emitInheritedRunnerTerminal(envelope, nil)
			require.NoError(t, err)
			require.Equal(t, control.code, code)
			_, err = output.Seek(0, io.SeekStart)
			require.NoError(t, err, "emission must leave stdout open")
			data, err := io.ReadAll(output)
			require.NoError(t, err)
			record, err := runnerterminal.Validate(data, nil)
			require.NoError(t, err)
			require.Equal(t, envelope.Outcome, record.Outcome)
			require.Equal(t, code, record.ExitCode)
			require.Equal(t, 1, bytes.Count(data, []byte{'\n'}))
		})
	}
}
