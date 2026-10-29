package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerTerminalAllOutcomesCLIHTTP(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, control := range []string{"success", "mixed-checks", "failed", "invalid", "version", "capability", "startup", "cancel", "timeout"} {
		t.Run(control, func(t *testing.T) {
			request, err := runnercontract.Parse(raw)
			require.NoError(t, err)
			want := runnerterminal.Success
			service := runnerunit.NewService(nil)
			switch control {
			case "mixed-checks":
				request.Input.Eval += "graders:\n  - name: eval-text\n    type: text\n    config:\n      contains: [hello]\n"
				request.Input.Task += "  output_not_contains:\n    - never-observed\n  output_contains_any:\n    - hello\ngraders:\n  - name: task-text\n    type: text\n    config:\n      contains: [hello]\n"
			case "failed":
				want = runnerterminal.EvaluationFailure
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- unavailable-result", 1)
			case "invalid":
				want = runnerterminal.InvalidInput
				request.Selection.Task = "neighbor"
			case "version":
				want = runnerterminal.UnsupportedCapability
				request.Versions = []string{"9.0"}
			case "capability":
				want = runnerterminal.UnsupportedCapability
				request.RequiredCapabilities = append(request.RequiredCapabilities, "inline.native-text")
			case "startup", "cancel", "timeout":
				want = map[string]runnerterminal.Outcome{"startup": runnerterminal.InfrastructureFailure,
					"cancel": runnerterminal.Canceled, "timeout": runnerterminal.Timeout}[control]
				cause := map[string]error{"startup": errors.New("injected host factory failure"),
					"cancel": context.Canceled, "timeout": context.DeadlineExceeded}[control]
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, cause
				})
			}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			for _, transport := range []string{"cli", "http"} {
				var output []byte
				if transport == "cli" {
					command := newRunnerCommandWithService(service)
					command.SetArgs([]string{"run"})
					command.SetIn(bytes.NewReader(data))
					var stdout bytes.Buffer
					command.SetOut(&stdout)
					command.SetErr(&bytes.Buffer{})
					err := command.ExecuteContext(t.Context())
					code, _ := want.ExitCode()
					if code == 0 {
						require.NoError(t, err)
					} else {
						exit, ok := errors.AsType[*ExitCodeError](err)
						require.True(t, ok)
						require.Equal(t, code, exit.Code)
					}
					output = stdout.Bytes()
				} else {
					mux := http.NewServeMux()
					webapi.RegisterRunnerRoutesWithService(mux, service)
					server := httptest.NewServer(mux)
					defer server.Close()
					response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(data))
					require.NoError(t, err)
					require.Equal(t, 200, response.StatusCode)
					output, err = io.ReadAll(response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
				}
				var envelope runnerterminal.Envelope
				require.NoError(t, json.Unmarshal(output, &envelope), "%s", output)
				require.Equal(t, want, envelope.Outcome)
				require.Equal(t, 1, bytes.Count(output, []byte{'\n'}))
				admitted := runnerunit.Validate(data)
				var binding *runnerterminal.Binding
				if admitted.Identity != nil {
					binding = &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
						GraderIDs: []string{"_output_contains"}}
					if control == "mixed-checks" {
						binding.GraderIDs = []string{"eval-text", "task-text", "_output_contains", "_output_not_contains", "_output_contains_any"}
						require.Len(t, envelope.Observation.Validations, len(binding.GraderIDs))
					}
				}
				_, err = runnerterminal.Validate(output, binding)
				require.NoError(t, err, "%s", output)
				if control == "version" {
					require.Equal(t, "unsupported_version", envelope.Diagnostics[0].Code)
				}
			}
		})
	}
}

func TestRunnerTerminalCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, control := range []string{"success", "failed", "invalid", "unsupported", "arguments", "flags", "oversize-flags", "runtime-filesystem"} {
		t.Run(control, func(t *testing.T) {
			data := bytes.Clone(raw)
			args, code := []string{"runner", "run"}, 0
			switch control {
			case "failed":
				data = bytes.Replace(data, []byte("- hello"), []byte("- not-observed"), 1)
				code = 1
			case "invalid":
				data, code = []byte(`{"kind":"bad"}`), 6
			case "unsupported":
				data, code = bytes.Replace(data, []byte(`"versions": ["1.0"]`), []byte(`"versions": ["9.0"]`), 1), 5
			case "arguments":
				args, code = append(args, "unexpected"), 6
			case "flags":
				args, code = append(args, "--unexpected"), 6
			case "oversize-flags":
				args, code = append(args, "--"+strings.Repeat("unknown", 1000)), 6
			case "runtime-filesystem":
				code = 2
			}
			child := exec.CommandContext(t.Context(), binary, args...)
			child.Dir = t.TempDir()
			child.Env = os.Environ()
			if control == "runtime-filesystem" {
				missing := child.Dir + "/nonexistent"
				child.Env = append(child.Env, "TMPDIR="+missing, "TMP="+missing, "TEMP="+missing)
			}
			child.Stdin = bytes.NewReader(data)
			var stderr bytes.Buffer
			child.Stderr = &stderr
			output, err := child.Output()
			if code == 0 {
				require.NoError(t, err, "%s", stderr.String())
			} else {
				exit, ok := errors.AsType[*exec.ExitError](err)
				require.True(t, ok, "%v: %s", err, stderr.String())
				require.Equal(t, code, exit.ExitCode())
			}
			var result runnerterminal.Envelope
			require.NoError(t, json.Unmarshal(output, &result), "%s", output)
			require.Equal(t, code, result.ExitCode)
			require.Equal(t, 1, bytes.Count(output, []byte{'\n'}))
			if code == 0 {
				require.Empty(t, stderr.String())
			} else {
				require.Contains(t, stderr.String(), "runner run: ")
				require.NotContains(t, stderr.String(), "Usage:")
			}
		})
	}
}

type shortTerminalWriter struct{ calls int }

func (w *shortTerminalWriter) Write(data []byte) (int, error) {
	w.calls++
	return len(data) / 2, nil
}

func TestRunnerTerminalPublicShortWriteNeverRetries(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	command := newRunnerCommand()
	command.SetArgs([]string{"run"})
	command.SetIn(bytes.NewReader(data))
	writer := &shortTerminalWriter{}
	command.SetOut(writer)
	command.SetErr(&bytes.Buffer{})
	exit, ok := errors.AsType[*ExitCodeError](command.Execute())
	require.True(t, ok)
	require.Equal(t, 2, exit.Code)
	require.ErrorIs(t, exit.Err, io.ErrShortWrite)
	require.Equal(t, 1, writer.calls)
}

func TestRunnerTerminalCancellationJoinsInputAndPreservesHandle(t *testing.T) {
	for _, control := range []string{"caller-managed", "pollable-file"} {
		t.Run(control, func(t *testing.T) {
			input, producer, err := os.Pipe()
			require.NoError(t, err)
			defer input.Close()    //nolint:errcheck
			defer producer.Close() //nolint:errcheck
			if err := input.SetReadDeadline(time.Time{}); err != nil {
				require.ErrorIs(t, err, os.ErrNoDeadline)
				if control == "pollable-file" {
					t.Skip("native supplied pipe has no deadline support; caller-managed ownership control still runs")
				}
				t.Log("nonpollable supplied pipe: owner supplies cancellation-aware read completion")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &runnerOwnerCanceledPipeReader{
				input: input, ctx: ctx, entered: make(chan struct{}), returned: make(chan struct{}),
			}
			factories := 0
			command := newRunnerCommandWithService(runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				factories++
				return execution.NewMockEngine(cfg.ModelID), nil
			}))
			command.SetArgs([]string{"run"})
			if control == "pollable-file" {
				command.SetIn(input)
			} else {
				command.SetIn(reader)
			}
			var output bytes.Buffer
			if control == "caller-managed" {
				command.SetOut(&runnerReadJoinedWriter{output: &output, returned: reader.returned})
			} else {
				command.SetOut(&output)
			}
			command.SetErr(&bytes.Buffer{})
			done := startRunnerPipeOperation(t, input, producer, func() error { return command.ExecuteContext(ctx) })
			var padding []byte
			if control == "pollable-file" {
				// As in the compiled Unix control, a write beyond native pipe
				// capacity proves the actual file reader has entered before cancellation.
				padding = append(bytes.Repeat([]byte(" "), 2<<20), '{')
				require.Less(t, len(padding), runnercontract.MaxRequestBytes)
				written := startRunnerPipeOperation(t, input, producer, func() error {
					n, err := producer.Write(padding)
					if err == nil && n != len(padding) {
						return io.ErrShortWrite
					}
					return err
				})
				require.NoError(t, awaitRunnerPipeOperation(t, written))
				cancel()
			} else {
				select {
				case <-reader.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("runner never entered supplied reader")
				}
				cancel()
				// Nonpollable supplied pipes require owner cooperation, not a runtime close.
				written := startRunnerPipeOperation(t, input, producer, func() error {
					n, err := producer.Write([]byte("{"))
					if err == nil && n != 1 {
						return io.ErrShortWrite
					}
					return err
				})
				require.NoError(t, awaitRunnerPipeOperation(t, written))
			}
			exit, ok := errors.AsType[*ExitCodeError](awaitRunnerPipeOperation(t, done))
			require.True(t, ok)
			require.Equal(t, 3, exit.Code)
			require.EqualError(t, exit.Err, "runner run: "+string(runnerterminal.Canceled))
			envelope, err := runnerterminal.Validate(output.Bytes(), nil)
			require.NoError(t, err)
			require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
			require.Equal(t, "input", envelope.FailureBoundary)
			require.Equal(t, "not_checked", envelope.Validation)
			require.Nil(t, envelope.Identity)
			require.Nil(t, envelope.Selection)
			require.Nil(t, envelope.Observation)
			require.Zero(t, envelope.EngineCalls)
			require.Zero(t, factories)
			require.Len(t, envelope.Diagnostics, 1)
			require.Contains(t, envelope.Diagnostics[0].Message, "context canceled")
			if control == "caller-managed" {
				require.Equal(t, 1, reader.reads, "the supplied read completed before terminal return")
				select {
				case <-reader.returned:
				default:
					t.Fatal("terminal return preceded supplied reader completion")
				}
			}
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))

			value := make([]byte, len("still-owned"))
			read := startRunnerPipeOperation(t, input, producer, func() error {
				if control == "pollable-file" {
					received, err := readRunnerOwnershipMarker(input, padding)
					if err != nil {
						return err
					}
					copy(value, received[len(received)-len(value):])
					return nil
				}
				_, err := io.ReadFull(input, value)
				return err
			})
			written := startRunnerPipeOperation(t, input, producer, func() error {
				n, err := producer.Write([]byte("still-owned"))
				if err == nil && n != len(value) {
					return io.ErrShortWrite
				}
				return err
			})
			require.NoError(t, awaitRunnerPipeOperation(t, written))
			require.NoError(t, awaitRunnerPipeOperation(t, read))
			require.Equal(t, "still-owned", string(value), "terminalization must not close or retain a reader on caller-owned input")
		})
	}
}

func readRunnerOwnershipMarker(input io.Reader, padding []byte) ([]byte, error) {
	const marker = "still-owned"
	// Cancellation may leave only a suffix of the known pre-cancel write.
	reader := bufio.NewReader(io.LimitReader(input, int64(len(padding)+len(marker))))
	received, err := reader.ReadBytes(marker[len(marker)-1])
	if err != nil {
		return nil, fmt.Errorf("reading bounded ownership marker: %w", err)
	}
	if !bytes.HasSuffix(received, []byte(marker)) {
		return nil, errors.New("retained input did not end with the exact ownership marker")
	}
	residual := received[:len(received)-len(marker)]
	if !bytes.HasSuffix(padding, residual) {
		return nil, errors.New("retained input was not a suffix of the known pre-cancel padding")
	}
	return received, nil
}

func TestRunnerTerminalOwnershipResidualPadding(t *testing.T) {
	padding := append(bytes.Repeat([]byte(" "), 2<<20), '{')
	withMarker := func(residual []byte) []byte {
		return append(bytes.Clone(residual), []byte("still-owned")...)
	}
	for _, control := range []struct {
		name  string
		wire  []byte
		valid bool
	}{
		{"zero", withMarker(nil), true},
		{"last-byte", withMarker(padding[len(padding)-1:]), true},
		{"partial", withMarker(padding[len(padding)-4096:]), true},
		{"maximal", withMarker(padding), true},
		{"unexpected-prefix", withMarker([]byte("unexpected{")), false},
		{"missing-final-padding-byte", withMarker([]byte(" ")), false},
		{"reordered-padding", withMarker([]byte("{ ")), false},
		{"wrong-marker", []byte("{other-owned"), false},
		{"truncated-marker", []byte("{still-owne"), false},
		{"missing-marker", bytes.Clone(padding), false},
		{"excess-padding", withMarker(append([]byte(" "), padding...)), false},
	} {
		t.Run(control.name, func(t *testing.T) {
			input := bytes.NewReader(control.wire)
			received, err := readRunnerOwnershipMarker(input, padding)
			require.LessOrEqual(t, len(control.wire)-input.Len(), len(padding)+len("still-owned"),
				"never read beyond the known residual-plus-marker bound")
			if control.valid {
				require.NoError(t, err)
				require.Equal(t, control.wire, received)
			} else {
				require.Error(t, err)
				require.Nil(t, received)
			}
		})
	}
}

func TestRunnerTerminalUnsupportedDeadlinePreservesCallerFile(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "canceled"}[canceled], func(t *testing.T) {
			input, err := os.CreateTemp(t.TempDir(), "runner-input")
			require.NoError(t, err)
			defer input.Close() //nolint:errcheck
			_, err = input.Write(raw)
			require.NoError(t, err)
			_, err = input.Seek(0, io.SeekStart)
			require.NoError(t, err)
			require.ErrorIs(t, input.SetReadDeadline(time.Time{}), os.ErrNoDeadline)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			command := newRunnerCommand()
			command.SetArgs([]string{"run"})
			command.SetIn(input)
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&bytes.Buffer{})
			err = command.ExecuteContext(ctx)
			var binding *runnerterminal.Binding
			if !canceled {
				admitted := runnerunit.Validate(raw)
				require.Equal(t, "ready", admitted.Status)
				binding = &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
					GraderIDs: []string{"_output_contains"}}
			}
			envelope, decodeErr := runnerterminal.Validate(output.Bytes(), binding)
			require.NoError(t, decodeErr)
			if canceled {
				exit, ok := errors.AsType[*ExitCodeError](err)
				require.True(t, ok)
				require.Equal(t, 3, exit.Code)
				require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
				require.Equal(t, "input", envelope.FailureBoundary)
				require.Nil(t, envelope.Observation)
				require.Zero(t, envelope.EngineCalls)
			} else {
				require.NoError(t, err)
				require.Equal(t, runnerterminal.Success, envelope.Outcome)
				require.Equal(t, 1, envelope.EngineCalls)
				require.NotNil(t, envelope.Observation)
			}
			_, err = input.Seek(0, io.SeekStart)
			require.NoError(t, err, "unsupported deadlines must not transfer file ownership")
			retained, err := io.ReadAll(input)
			require.NoError(t, err)
			require.Equal(t, raw, retained)
		})
	}
}

func TestRunnerTerminalCancellationWaitsForSuppliedReader(t *testing.T) {
	// Signal registration uses process-wide goroutines outside synctest's clock.
	_, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader := &runnerBlockedReader{
			entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}),
		}
		defer func() {
			select {
			case <-reader.release:
			default:
				close(reader.release)
			}
		}()
		command := newRunnerCommand()
		command.SetArgs([]string{"run"})
		command.SetIn(reader)
		var output bytes.Buffer
		command.SetOut(&runnerReadJoinedWriter{output: &output, returned: reader.returned})
		command.SetErr(&bytes.Buffer{})
		done := make(chan error, 1)
		go func() { done <- command.ExecuteContext(ctx) }()
		<-reader.entered
		cancel()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("terminal return left the supplied read unjoined")
		default:
		}
		close(reader.release)
		exit, ok := errors.AsType[*ExitCodeError](<-done)
		require.True(t, ok)
		require.Equal(t, 3, exit.Code)
		envelope, err := runnerterminal.Validate(output.Bytes(), nil)
		require.NoError(t, err)
		require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
		require.Equal(t, "input", envelope.FailureBoundary)
		require.Nil(t, envelope.Observation)
		require.Zero(t, envelope.EngineCalls)
	})
}

type runnerBlockedReader struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (r *runnerBlockedReader) Read([]byte) (int, error) {
	defer close(r.returned)
	close(r.entered)
	<-r.release
	return 0, io.EOF
}

type runnerOwnerCanceledPipeReader struct {
	input    *os.File
	ctx      context.Context
	entered  chan struct{}
	returned chan struct{}
	reads    int
}

func (r *runnerOwnerCanceledPipeReader) Read(data []byte) (int, error) {
	defer close(r.returned)
	r.reads++
	if r.reads == 1 {
		close(r.entered)
	}
	n, err := r.input.Read(data)
	if r.ctx.Err() != nil {
		return n, io.EOF
	}
	return n, err
}

func (r *runnerOwnerCanceledPipeReader) Close() error { return r.input.Close() }

type runnerReadJoinedWriter struct {
	output   io.Writer
	returned <-chan struct{}
}

func (w *runnerReadJoinedWriter) Write(data []byte) (int, error) {
	select {
	case <-w.returned:
		return w.output.Write(data)
	default:
		return 0, errors.New("terminal output preceded supplied reader completion")
	}
}

func startRunnerPipeOperation(t *testing.T, input, producer *os.File, operation func() error) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		result <- operation()
	}()
	t.Cleanup(func() {
		// Cleanup unblocks only invocation-owned handles, including on assertion failure.
		_ = input.Close()
		_ = producer.Close()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Error("owned pipe operation survived handle cleanup")
		}
	})
	return result
}

func awaitRunnerPipeOperation(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("owned pipe operation did not complete")
		return nil
	}
}
