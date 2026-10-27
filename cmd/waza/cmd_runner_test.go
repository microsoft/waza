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
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func runnerFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../internal/runnercontract/testdata/request.json")
	require.NoError(t, err)
	return data
}

func TestRunnerCLIHTTPParity(t *testing.T) {
	for _, name := range []string{"success", "evaluation-failure", "unsupported", "invalid", "malformed"} {
		t.Run(name, func(t *testing.T) {
			var req runnercontract.Request
			require.NoError(t, json.Unmarshal(runnerFixture(t), &req))
			expected := runnercontract.Success
			switch name {
			case "evaluation-failure":
				req.Input.Task += "  output_not_contains:\n    - hello\n"
				expected = runnercontract.EvaluationFailure
			case "unsupported":
				req.Versions = []string{"9.0"}
				expected = runnercontract.UnsupportedCapability
			case "invalid":
				req.Selection.Task = "neighbor"
				expected = runnercontract.InvalidRequest
			}
			data, err := json.Marshal(req)
			require.NoError(t, err)
			if name == "malformed" {
				data = []byte("{")
				expected = runnercontract.InvalidRequest
			}
			cmd := newRootCommand()
			cmd.SetArgs([]string{"runner", "run"})
			cmd.SetIn(bytes.NewReader(data))
			var out, stderr bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&stderr)
			err = cmd.Execute()
			if expected != runnercontract.Success {
				var exit *ExitCodeError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, expected.ExitCode(), exit.Code)
			} else {
				require.NoError(t, err)
			}
			actual, err := runnercontract.ParseResult(out.Bytes())
			require.NoError(t, err)
			require.Equal(t, expected, actual.Outcome)
			require.Empty(t, stderr.String())
			mux := http.NewServeMux()
			webapi.RegisterRunnerRoutes(mux, t.TempDir())
			w := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
			httpRequest.Host = "localhost"
			httpRequest.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(w, httpRequest)
			api, err := runnercontract.ParseResult(w.Body.Bytes())
			require.NoError(t, err)
			require.Equal(t, actual.Identity, api.Identity)
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				require.Equal(t, runnercontract.InfrastructureFailure, api.Outcome)
				require.Equal(t, 2, api.ExitCode)
				require.Equal(t, "publication_failed", api.Diagnostics[len(api.Diagnostics)-1].Code)
				require.Contains(t, api.Diagnostics[len(api.Diagnostics)-1].Message, "unsupported on this platform")
				return
			}
			require.Equal(t, actual.Outcome, api.Outcome)
			require.Equal(t, actual.ExitCode, api.ExitCode)
			require.Equal(t, actual.Diagnostics, api.Diagnostics)
		})
	}
}

func TestRunnerCrossProcessIdentityAndExitCodes(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, name := range []string{"success", "retry", "evaluation-failure", "unsupported", "invalid", "infrastructure", "timeout"} {
		t.Run(name, func(t *testing.T) {
			var request runnercontract.Request
			require.NoError(t, json.Unmarshal(runnerFixture(t), &request))
			expected := runnercontract.Success
			switch name {
			case "evaluation-failure":
				request.Input.Task += "  output_not_contains:\n    - hello\n"
				expected = runnercontract.EvaluationFailure
			case "unsupported":
				request.Versions = []string{"9.0"}
				expected = runnercontract.UnsupportedCapability
			case "invalid":
				request.Selection.Task = "adjacent"
				expected = runnercontract.InvalidRequest
			case "infrastructure":
				expected = runnercontract.InfrastructureFailure
			case "timeout":
				request.TimeoutMS = 1
				expected = runnercontract.Timeout
			}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestRunnerProcessHelper$")
			command.Dir = t.TempDir()
			command.Env = append(runnerProcessEnvironment(), "WAZA_RUNNER_PROCESS_HELPER=1", "WAZA_RUNNER_FAULT="+name)
			command.Stdin = bytes.NewReader(data)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err = command.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, stderr.String())
				code = exit.ExitCode()
			}
			require.Equal(t, expected.ExitCode(), code, stderr.String())
			result, err := runnercontract.ParseResult(stdout.Bytes())
			require.NoError(t, err, stdout.String())
			require.Equal(t, expected, result.Outcome)
			if expected == runnercontract.Success {
				frozen, err := os.ReadFile("../../internal/runnercontract/testdata/identity.json")
				require.NoError(t, err)
				var id runnercontract.Identity
				require.NoError(t, json.Unmarshal(frozen, &id))
				require.Equal(t, id, *result.Identity)
				require.Equal(t, 2, result.Observation.RunNumber)
				require.Equal(t, 2, result.Observation.Attempts)
			}
			decoder := json.NewDecoder(&stdout)
			var value any
			require.NoError(t, decoder.Decode(&value))
			require.ErrorIs(t, decoder.Decode(&value), io.EOF)
		})
	}
}

func runnerProcessEnvironment() []string {
	env := []string{"ENABLE_COPILOT_TESTS=false", "WAZA_NO_UPDATE_CHECK=1"}
	for _, key := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "SystemRoot", "SYSTEMROOT", "PATH", "TMPDIR", "TMP", "TEMP", "GOTMPDIR", "COPILOT_CLI_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func TestRunnerProcessHelper(t *testing.T) {
	if os.Getenv("WAZA_RUNNER_PROCESS_HELPER") != "1" {
		return
	}
	fault := os.Getenv("WAZA_RUNNER_FAULT")
	if fault == "infrastructure" || fault == "timeout" {
		newPublicRunnerService = func() *runnercontract.Service {
			return runnercontract.NewService(runnercontract.WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
				return &runnerFaultEngine{mode: fault}, nil
			}))
		}
	}
	os.Args = []string{"waza", "runner", "run"}
	main()
	os.Exit(0)
}

type runnerFaultEngine struct{ mode string }

func (*runnerFaultEngine) Initialize(context.Context) error       { return nil }
func (*runnerFaultEngine) Shutdown(context.Context) error         { return nil }
func (*runnerFaultEngine) SessionUsage(string) *models.UsageStats { return nil }
func (e *runnerFaultEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if e.mode == "timeout" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("injected infrastructure failure")
}

type runnerErrorReader struct{}

func (runnerErrorReader) Read([]byte) (int, error) { return 0, errors.New("injected input failure") }

type runnerErrorWriter struct{}

func (runnerErrorWriter) Write([]byte) (int, error) { return 0, errors.New("injected output failure") }

func TestRunnerInputAndOutputFailures(t *testing.T) {
	cmd := newRunnerCommand()
	cmd.SetArgs([]string{"run"})
	cmd.SetIn(runnerErrorReader{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	var exit *ExitCodeError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 6, exit.Code)
	result, err := runnercontract.ParseResult(out.Bytes())
	require.NoError(t, err)
	require.Equal(t, runnercontract.InvalidRequest, result.Outcome)
	require.Contains(t, result.Diagnostics[0].Message, "injected input failure")
	cmd = newRootCommand()
	cmd.SetArgs([]string{"runner", "run"})
	cmd.SetIn(bytes.NewReader(runnerFixture(t)))
	cmd.SetOut(runnerErrorWriter{})
	err = cmd.Execute()
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 2, exit.Code)
	require.ErrorContains(t, err, "terminal result could not be written")
}

func TestRunnerInvocationErrorsProduceOneEnvelope(t *testing.T) {
	for _, args := range [][]string{{"runner", "run", "adjacent.yaml"}, {"runner", "run", "--trial", "2"}} {
		cmd := newRootCommand()
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		err := cmd.Execute()
		var exit *ExitCodeError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, 6, exit.Code)
		result, err := runnercontract.ParseResult(out.Bytes())
		require.NoError(t, err, out.String())
		require.Equal(t, runnercontract.InvalidRequest, result.Outcome)
	}
}

func TestRunnerAdapterCommand(t *testing.T) {
	dir := t.TempDir()
	var req runnercontract.Request
	require.NoError(t, json.Unmarshal(runnerFixture(t), &req))
	evalPath, taskPath := dir+"/eval.yaml", dir+"/task.yaml"
	require.NoError(t, os.WriteFile(evalPath, []byte(req.Input.Eval), 0o600))
	require.NoError(t, os.WriteFile(taskPath, []byte(req.Input.Task), 0o600))
	cmd := newRootCommand()
	cmd.SetArgs([]string{"runner", "adapt", "--eval", evalPath, "--task", taskPath,
		"--run-key", req.RunKey, "--shard", req.Selection.Shard, "--task-id", req.Selection.Task,
		"--trial", "2", "--attempt", "2"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	adapted, err := runnercontract.Parse(out.Bytes())
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), out.Bytes())
	require.Equal(t, runnercontract.Success, result.Outcome)
	require.Equal(t, req.Input, adapted.Input)
	require.Equal(t, req.Selection, *result.Resolved)
	runner, _, err := cmd.Find([]string{"runner", "adapt"})
	require.NoError(t, err)
	require.False(t, shouldRunUpdateCheck(runner, false))
	require.NotContains(t, strings.ToLower(out.String()), "scheduler")
}

func TestRunnerCapabilitiesAndOfflineResultValidation(t *testing.T) {
	cmd := newRootCommand()
	cmd.SetArgs([]string{"runner", "capabilities"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutes(mux, t.TempDir())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runner/capabilities", nil))
	require.JSONEq(t, out.String(), w.Body.String())
	result := runnercontract.NewService().Run(t.Context(), runnerFixture(t))
	out.Reset()
	require.NoError(t, runnercontract.Write(&out, &result))
	for _, valid := range []bool{true, false} {
		cmd = newRootCommand()
		cmd.SetArgs([]string{"runner", "validate-result"})
		var output bytes.Buffer
		cmd.SetOut(&output)
		if valid {
			cmd.SetIn(bytes.NewReader(out.Bytes()))
			require.NoError(t, cmd.Execute())
		} else {
			cmd.SetIn(strings.NewReader(`{"schema_version":"1.0"}`))
			require.Error(t, cmd.Execute())
		}

		require.Empty(t, output.String())
	}
}

func TestRunnerDashboardAliasKeepsLabEntrypoint(t *testing.T) {
	command, _, err := newRootCommand().Find([]string{"dashboard"})
	require.NoError(t, err)
	require.Equal(t, "serve", command.Name())
	require.NotNil(t, command.Flags().Lookup("lab"))
}
