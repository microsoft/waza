package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
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
	input, producer, err := os.Pipe()
	require.NoError(t, err)
	defer input.Close()    //nolint:errcheck
	defer producer.Close() //nolint:errcheck
	ctx, cancel := context.WithCancel(t.Context())
	command := newRunnerCommand()
	command.SetArgs([]string{"run"})
	command.SetIn(input)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	done := make(chan error, 1)
	go func() { done <- command.ExecuteContext(ctx) }()
	_, err = producer.Write([]byte("{"))
	require.NoError(t, err)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("terminal return left input blocked")
	}
	require.Contains(t, output.String(), `"outcome":"`+string(runnerterminal.Canceled)+`"`)
	require.NoError(t, input.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = producer.Write([]byte("still-owned"))
	require.NoError(t, err)
	value := make([]byte, 32)
	n, err := input.Read(value)
	require.NoError(t, err)
	require.Contains(t, string(value[:n]), "still-owned", "terminalization must not close caller-owned input")
}
