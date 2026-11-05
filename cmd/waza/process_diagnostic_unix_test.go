//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Force caller-owned file closure after the deadline probe/registration and
// before cancellation, without sleeps or production-only injection hooks.
type closeBeforeRunnerCancelContext struct {
	context.Context
	input      *os.File
	cancel     context.CancelFunc
	registered func()
	triggered  atomic.Bool
	closeErr   error
}

func (*closeBeforeRunnerCancelContext) Value(any) any { return nil }

func (c *closeBeforeRunnerCancelContext) AfterFunc(callback func()) func() bool {
	c.registered = callback
	return func() bool { return false }
}

func (c *closeBeforeRunnerCancelContext) Err() error {
	if c.registered != nil && c.triggered.CompareAndSwap(false, true) {
		c.closeErr = c.input.Close()
		c.cancel()
		c.registered()
	}
	return c.Context.Err()
}

func TestProcessDiagnosticsRunnerInterruptErrorIsReturned(t *testing.T) {
	input, producer, err := os.Pipe()
	require.NoError(t, err)
	defer input.Close()    //nolint:errcheck
	defer producer.Close() //nolint:errcheck
	require.NoError(t, input.SetReadDeadline(time.Time{}), "native supplied file must support the production deadline path")
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx := &closeBeforeRunnerCancelContext{Context: parent, input: input, cancel: cancel}
	data, err := readRunnerInput(ctx, input)
	require.Empty(t, data)
	require.True(t, ctx.triggered.Load(), "fault must occur after the actual callback registration")
	require.NoError(t, ctx.closeErr, "only the caller closes its own file")
	require.ErrorIs(t, err, context.Canceled)
	deadlineErr := input.SetReadDeadline(time.Now())
	require.Error(t, deadlineErr)
	require.ErrorIs(t, err, deadlineErr, "retain the native deadline failure, not a guessed os.ErrClosed")
	require.ErrorContains(t, err, "runner input interrupted")
	require.ErrorContains(t, err, "interrupting runner input")
	require.ErrorContains(t, err, "resetting runner input deadline")
	_, err = producer.Stat()
	require.NoError(t, err, "input error must not close caller's producer")
}

// Fill through one-byte EAGAIN as well as a zero-ready poll: a diagnostic
// smaller than the bulk fill size must not accidentally fit in the pipe.
func fillProcessDiagnosticPipe(t *testing.T, writer *os.File) []byte {
	t.Helper()
	filled := fillRunnerPipe(t, writer)
	fd := int(writer.Fd())
	require.NoError(t, unix.SetNonblock(fd, true))
	for {
		n, err := unix.Write(fd, []byte("x"))
		if n > 0 {
			filled = append(filled, 'x')
		}
		if errors.Is(err, unix.EAGAIN) {
			require.LessOrEqual(t, n, 0)
			break
		}
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 0)
	require.NoError(t, err)
	require.Zero(t, ready, "the native diagnostic pipe must not be write-ready")
	require.NoError(t, unix.SetNonblock(fd, false))
	t.Logf("retained undrained native pipe: %d bytes; one-byte write EAGAIN; POLLOUT ready=0", len(filled))
	return filled
}

func TestProcessDiagnosticsRealProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.Equal(t, "ready", admitted.Status)
	require.NotNil(t, admitted.Resolved)
	require.NotNil(t, admitted.Identity)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity, GraderIDs: []string{"_output_contains"}}
	for _, stderrMode := range []string{"full", "full-nonblocking", "writable", "broken"} {
		for _, control := range []string{"broken-stdout", "blocked-stdout", "cancel-term", "cancel-int", "ordinary-error", "success", "invalid-input", "evaluation-failed", "unsupported", "validate-success"} {
			t.Run(stderrMode+"/"+control, func(t *testing.T) {
				stderrReader, stderrWriter, err := os.Pipe()
				require.NoError(t, err)
				defer stderrReader.Close() //nolint:errcheck
				defer stderrWriter.Close() //nolint:errcheck
				stderrPeer := duplicateRunnerFD(t, stderrWriter)
				defer stderrPeer.Close() //nolint:errcheck
				stderrFD, stderrPeerFD := stderrWriter.Fd(), stderrPeer.Fd()
				var filledStderr []byte
				var stderr bytes.Buffer
				switch stderrMode {
				case "full", "full-nonblocking":
					filledStderr = fillProcessDiagnosticPipe(t, stderrWriter)
					if stderrMode == "full-nonblocking" {
						require.NoError(t, unix.SetNonblock(int(stderrPeerFD), true))
					}
				case "broken":
					require.NoError(t, stderrReader.Close())
				}
				stderrFlags, err := unix.FcntlInt(stderrPeerFD, unix.F_GETFL, 0)
				require.NoError(t, err)
				if stderrMode == "full-nonblocking" {
					require.NotZero(t, stderrFlags&unix.O_NONBLOCK)
				} else {
					require.Zero(t, stderrFlags&unix.O_NONBLOCK)
				}

				args, code := []string{"runner", "run"}, 0
				var wantDiagnostic string
				switch control {
				case "ordinary-error":
					args, code = []string{"--stderr-exit-control"}, 2
					wantDiagnostic = "Error: unknown flag: --stderr-exit-control\nunknown flag: --stderr-exit-control\n"
				case "cancel-term", "cancel-int":
					code = 3
					wantDiagnostic = "runner run: " + string(runnerterminal.Canceled) + "\n"
				case "invalid-input":
					code = 6
					wantDiagnostic = "runner run: " + string(runnerterminal.InvalidInput) + "\n"
				case "evaluation-failed":
					code = 1
					wantDiagnostic = "runner run: " + string(runnerterminal.EvaluationFailure) + "\n"
				case "unsupported":
					code = 5
					wantDiagnostic = "runner run: " + string(runnerterminal.UnsupportedCapability) + "\n"
				case "broken-stdout":
					code = 2
					wantDiagnostic = "writing terminal output: write runner-terminal-output: broken pipe\n"
				case "blocked-stdout":
					code = 2
					wantDiagnostic = "writing terminal output: i/o timeout\n"
				case "validate-success":
					args = []string{"runner", "validate"}
				}
				// No CommandContext/watchdog: only natural child exit is accepted.
				child := exec.Command(binary, args...)
				child.Dir = t.TempDir()
				child.WaitDelay = time.Second
				child.Stdin = bytes.NewReader(request)
				var stdout bytes.Buffer
				child.Stdout = &stdout
				// NewFile's blocking peer avoids os/exec's Fd call changing an
				// os.Pipe writer's Go-managed nonblocking flags during setup.
				child.Stderr = stderrPeer
				if stderrMode == "writable" {
					child.Stderr = &stderr
				}
				switch control {
				case "invalid-input":
					child.Stdin = bytes.NewBufferString(`{"kind":"bad"}`)
				case "evaluation-failed":
					data := bytes.Replace(request, []byte("- hello"), []byte("- not-observed"), 1)
					require.NotEqual(t, request, data)
					child.Stdin = bytes.NewReader(data)
				case "unsupported":
					data := bytes.Replace(request, []byte(`"versions": ["1.0"]`), []byte(`"versions": ["9.0"]`), 1)
					require.NotEqual(t, request, data)
					child.Stdin = bytes.NewReader(data)
				}

				var stdoutReader, stdoutWriter, stdoutPeer, inputReader, producer *os.File
				var filledStdout []byte
				if control == "broken-stdout" || control == "blocked-stdout" {
					stdoutReader, stdoutWriter, err = os.Pipe()
					require.NoError(t, err)
					defer stdoutReader.Close() //nolint:errcheck
					defer stdoutWriter.Close() //nolint:errcheck
					stdoutPeer = duplicateRunnerFD(t, stdoutWriter)
					defer stdoutPeer.Close() //nolint:errcheck
					if control == "broken-stdout" {
						require.NoError(t, stdoutReader.Close())
					} else {
						filledStdout = fillProcessDiagnosticPipe(t, stdoutWriter)
					}
					child.Stdout = stdoutWriter
				}
				if control == "cancel-term" || control == "cancel-int" {
					inputReader, producer, err = os.Pipe()
					require.NoError(t, err)
					defer inputReader.Close() //nolint:errcheck
					defer producer.Close()    //nolint:errcheck
					child.Stdin = inputReader
				}
				require.NoError(t, child.Start())
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				joined := false
				t.Cleanup(func() {
					if !joined {
						killErr := child.Process.Kill()
						if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
							t.Errorf("failure cleanup kill: %v", killErr)
						}
						cleanupErr := <-done
						t.Logf("failure-only cleanup joined Wait=%v; state=%v; captured stdout=%q", cleanupErr, child.ProcessState, stdout.String())
					}
				})
				if producer != nil {
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					payload := append(bytes.Repeat([]byte(" "), 2<<20), '{')
					n, writeErr := writeRunnerStartupInput(ctx, producer, payload)
					require.NoError(t, writeErr, "readiness requires real consumption, not process liveness")
					require.Equal(t, len(payload), n)
					signal := syscall.SIGTERM
					if control == "cancel-int" {
						signal = syscall.SIGINT
					}
					require.NoError(t, child.Process.Signal(signal))
				}
				start := time.Now()
				var waitErr error
				select {
				case waitErr = <-done:
					joined = true
				case <-time.After(10 * time.Second):
					t.Fatal("natural process exit exceeded existing 10s budget; stderr is still full and undrained; failure cleanup will kill and join")
				}
				t.Logf("natural exit=%d, wait=%s; retained stderr full/undrained=%t", child.ProcessState.ExitCode(), time.Since(start), strings.HasPrefix(stderrMode, "full"))
				if code == 0 {
					require.NoError(t, waitErr)
				} else {
					exit, ok := errors.AsType[*exec.ExitError](waitErr)
					require.True(t, ok, "%v", waitErr)
					require.Equal(t, code, exit.ExitCode())
				}
				peerFlags, flagErr := unix.FcntlInt(stderrPeerFD, unix.F_GETFL, 0)
				require.NoError(t, flagErr)
				require.Equal(t, stderrFlags, peerFlags, "stderr peer flags must be unchanged")
				writerFlags, flagErr := unix.FcntlInt(stderrFD, unix.F_GETFL, 0)
				require.NoError(t, flagErr)
				require.Equal(t, stderrFlags, writerFlags, "caller stderr handle must remain open and unchanged")
				if strings.HasPrefix(stderrMode, "full") {
					require.NoError(t, stderrWriter.Close())
					require.NoError(t, stderrPeer.Close())
					got, readErr := io.ReadAll(stderrReader)
					require.NoError(t, readErr)
					require.Equal(t, filledStderr, got, "full stderr must not gain a retry or require draining to exit")
				} else if stderrMode == "writable" {
					require.Equal(t, wantDiagnostic, stderr.String(), "preserve all ordinary Cobra/main diagnostic bytes")
				}
				if stdoutWriter != nil {
					require.Zero(t, runnerFDFlags(t, stdoutPeer)&unix.O_NONBLOCK)
					require.Zero(t, runnerFDFlags(t, stdoutWriter)&unix.O_NONBLOCK)
					if control == "blocked-stdout" {
						require.NoError(t, stdoutWriter.Close())
						require.NoError(t, stdoutPeer.Close())
						got, readErr := io.ReadAll(stdoutReader)
						require.NoError(t, readErr)
						require.Equal(t, filledStdout, got, "failed output cannot append a terminal fallback")
					}
					require.Empty(t, stdout.Bytes())
				} else if control == "ordinary-error" {
					require.Empty(t, stdout.Bytes())
				} else {
					require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
					if control == "validate-success" {
						var receipt runnerunit.Report
						require.NoError(t, json.Unmarshal(stdout.Bytes(), &receipt))
						require.Equal(t, "waza.runner-unit", receipt.Kind)
						require.Equal(t, "1.0", receipt.Version)
						require.Equal(t, "ready", receipt.Status)
						require.Equal(t, "valid", receipt.Validation)
						require.NotNil(t, receipt.Identity)
						require.NotNil(t, receipt.Resolved)
						require.Zero(t, receipt.Calls)
						require.Nil(t, receipt.Observation)
					} else {
						var expected *runnerterminal.Binding
						switch control {
						case "success":
							expected = binding
						case "evaluation-failed":
							data := bytes.Replace(request, []byte("- hello"), []byte("- not-observed"), 1)
							failedAdmission := runnerunit.Validate(data)
							require.Equal(t, "ready", failedAdmission.Status)
							require.NotNil(t, failedAdmission.Resolved)
							expected = &runnerterminal.Binding{Selection: *failedAdmission.Resolved, Identity: failedAdmission.Identity, GraderIDs: []string{"_output_contains"}}
						}
						envelope, validateErr := runnerterminal.Validate(stdout.Bytes(), expected)
						require.NoError(t, validateErr)
						require.Equal(t, code, envelope.ExitCode)
						switch control {
						case "cancel-term", "cancel-int":
							require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
							require.Equal(t, "input", envelope.FailureBoundary)
							require.Nil(t, envelope.Identity)
						case "invalid-input":
							require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
						case "evaluation-failed":
							require.Equal(t, runnerterminal.EvaluationFailure, envelope.Outcome)
							require.NotEmpty(t, envelope.Observation.Validations)
						case "unsupported":
							require.Equal(t, runnerterminal.UnsupportedCapability, envelope.Outcome)
						default:
							require.Equal(t, runnerterminal.Success, envelope.Outcome)
							require.NotNil(t, envelope.Observation)
						}
					}
				}
				if producer != nil {
					_, err = producer.Stat()
					require.NoError(t, err, "cancellation must not close caller stdin producer")
					require.Zero(t, runnerFDFlags(t, inputReader)&unix.O_NONBLOCK)
				}
			})
		}
	}
}
