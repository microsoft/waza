package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestRecoveryMCPExamplesGradeObservedFaults(t *testing.T) {
	base := testutil.RepoFile(t, "examples", "scenarios", "recovery", "mcp")
	spec, err := models.LoadEvalSpec(filepath.Join(base, "eval.yaml"))
	require.NoError(t, err)
	cfg, err := mcpmock.FromEvalConfigWithVersion(spec.MCPMocks[0], base, spec.SchemaVersion)
	require.NoError(t, err)
	for _, test := range []struct {
		operation, report string
		calls             int
	}{
		{"transient", "Recovered after one transient error.", 2},
		{"permanent", "Unable to complete: dependency error.", 1},
		{"denied", "Unable to complete: permission denied.", 1},
		{"timeout", "Unable to complete: dependency timed out.", 1},
		{"malformed", "Unable to complete: response malformed.", 1},
		{"unmatched", "Unable to complete: no matching fixture.", 1},
	} {
		t.Run(test.operation, func(t *testing.T) {
			task, err := models.LoadTestCase(filepath.Join(base, "tasks", test.operation+".yaml"))
			require.NoError(t, err)
			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.txt"), []byte(test.report), 0600))
			makeTrace := func(operation string, count int) []models.ToolEvent {
				server := mcpmock.NewServer(cfg, nil)
				events := []models.ToolEvent{}
				for index := range count {
					params, err := json.Marshal(map[string]any{"name": "request", "arguments": map[string]any{"operation": operation}})
					require.NoError(t, err)
					response := server.HandleRequest(t.Context(), &jsonrpc.Request{Method: "tools/call", Params: params, ID: json.RawMessage(fmt.Sprint(index))})
					require.Nil(t, response.Error)
					result, ok := response.Result.(map[string]any)
					require.True(t, ok)
					failed, _ := result["isError"].(bool)
					event := models.ToolEvent{ToolName: "dependency-request", Args: map[string]any{"operation": operation}, Result: result, Success: !failed}
					if failed {
						data, err := json.Marshal(result)
						require.NoError(t, err)
						event.Error = string(data)
					}
					events = append(events, event)
				}
				// File-writing and other non-dependency tools do not count as retries.
				return append(events, models.ToolEvent{ToolName: "create", Args: map[string]any{"path": "report.txt"}, Success: true})
			}
			for _, trace := range []struct {
				name, operation string
				count           int
				passed          bool
			}{
				{"compliant", test.operation, test.calls, true},
				{"report only", test.operation, 0, false},
				{"wrong operation", "supported", 1, false},
				{"extra retry", test.operation, test.calls + 1, false},
			} {
				t.Run(trace.name, func(t *testing.T) {
					graded, err := gradeRunWithJudge(t.Context(), spec, task, &models.RunResult{ToolEvents: makeTrace(trace.operation, trace.count)}, workspace, "", io.Discard, false, evidenceGradeSource{})
					require.NoError(t, err)
					require.NotEmpty(t, graded.Validations)
					require.Equal(t, trace.passed, graded.Status == models.StatusPassed, "%+v", graded.Validations)
				})
			}
			if test.calls == 2 {
				t.Run("skipped retry", func(t *testing.T) {
					graded, err := gradeRunWithJudge(t.Context(), spec, task, &models.RunResult{ToolEvents: makeTrace(test.operation, 1)}, workspace, "", io.Discard, false, evidenceGradeSource{})
					require.NoError(t, err)
					require.Equal(t, models.StatusFailed, graded.Status)
				})
			}
		})
	}
}

func TestRecoveryCLIExampleGradesArgumentsAndExitSequence(t *testing.T) {
	base := testutil.RepoFile(t, "examples", "scenarios", "recovery", "cli")
	spec, err := models.LoadEvalSpec(filepath.Join(base, "eval.yaml"))
	require.NoError(t, err)
	task, err := models.LoadTestCase(filepath.Join(base, "tasks", "transient.yaml"))
	require.NoError(t, err)
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("Recovered after one transient error."), 0600))
	call := func(code *int, args ...string) models.CommandInvocation {
		return models.CommandInvocation{Command: "az", Args: args, ExitCode: code, ResponseIndex: 0}
	}

	args := []string{"account", "show", "--output", "json"}
	for _, test := range []struct {
		name   string
		calls  []models.CommandInvocation
		passed bool
	}{
		{"compliant", []models.CommandInvocation{call(new(1), args...), call(new(0), args...)}, true},
		{"report only", nil, false},
		{"wrong args", []models.CommandInvocation{call(new(127), "wrong"), call(new(127), "wrong")}, false},
		{"never succeeded", []models.CommandInvocation{call(new(1), args...), call(new(1), args...)}, false},
		{"incomplete attempt", []models.CommandInvocation{call(nil, args...), call(new(0), args...)}, false},
		{"extra retry", []models.CommandInvocation{call(new(1), args...), call(new(0), args...), call(new(0), args...)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			graded, err := gradeRunWithJudge(t.Context(), spec, task, &models.RunResult{CommandInvocations: test.calls}, workspace, "", io.Discard, false, evidenceGradeSource{})
			require.NoError(t, err)
			require.NotEmpty(t, graded.Validations)
			require.Equal(t, test.passed, graded.Status == models.StatusPassed, "%+v", graded.Validations)
		})
	}
}

func TestRecoveryExternalMCPAdmissionThroughRun(t *testing.T) {
	for _, test := range []struct {
		name, executor, response, inline string
		valid                            bool
	}{
		{"empty sequence", "mock", `{"sequence":[]}`, "", false},
		{"excessive delay", "mock", `{"sequence":[{"return":"ready","delay_ms":300001}]}`, "", false},
		{"SDK rejected before initialization", "copilot-sdk", `{"sequence":[]}`, "", false},
		{"null success", "mock", `{"sequence":[{"return":null}]}`, "", true},
		{"maximum delay", "mock", `{"sequence":[{"return":"ready","delay_ms":300000}]}`, "", true},
		{"discarded invalid fixture", "mock", `{"sequence":[]}`, "\n    tools:\n      request:\n        responses: [{return: ready}]", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetRunGlobals()
			dir := t.TempDir()
			t.Setenv("COPILOT_CLI_PATH", filepath.Join(dir, "must-not-launch"))
			eval := fmt.Sprintf(`schemaVersion: "2.0"
scenario: recovery
name: External admission
config: {executor: %s, model: mock, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
mcp_mocks:
  - name: dependency
    fixtures: mocks%s
`, test.executor, test.inline)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "request.json"), []byte(`{"responses":[`+test.response+`]}`), 0600))
			path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: probe\nname: Probe\ninputs: {prompt: inspect}\n"), 0600))
			cmd := newRunCommand()
			cmd.SetArgs([]string{path, "--context-dir", dir, "--output", output})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if !test.valid {
				require.ErrorContains(t, err, "MCP mock admission")
				require.NoFileExists(t, output)
				return
			}
			require.NoError(t, err)
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			var outcome models.EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &outcome))
			require.Len(t, outcome.TestOutcomes, 1)
			require.NotEmpty(t, outcome.TestOutcomes[0].Runs)
			require.Equal(t, []models.DependencyMode{{Kind: "mcp", Name: "dependency", Mode: "mocked"}}, outcome.TestOutcomes[0].Runs[0].DependencyModes)
		})
	}
}

func TestRecoveryMCPDelayAdmissionThroughRun(t *testing.T) {
	for _, delay := range []int{0, 300000, 300001} {
		t.Run(fmt.Sprint(delay), func(t *testing.T) {
			resetRunGlobals()
			dir := t.TempDir()
			eval := fmt.Sprintf(`schemaVersion: "2.0"
scenario: recovery
name: Delay admission
config: {executor: mock, model: mock, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
mcp_mocks:
  - name: dependency
    tools:
      request:
        responses:
          - sequence: [{return: ready, delay_ms: %d}]
`, delay)
			path := filepath.Join(dir, "eval.yaml")
			output := filepath.Join(dir, "results.json")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: probe\nname: Probe\ninputs: {prompt: inspect}\n"), 0600))
			cmd := newRunCommand()
			cmd.SetArgs([]string{path, "--context-dir", dir, "--output", output})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if delay > 300000 {
				require.ErrorContains(t, err, "delay_ms must be between 0 and 300000")
				require.NoFileExists(t, output)
			} else {
				require.NoError(t, err)
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				var outcome models.EvaluationOutcome
				require.NoError(t, json.Unmarshal(data, &outcome))
				require.Len(t, outcome.TestOutcomes, 1)
				require.NotEmpty(t, outcome.TestOutcomes[0].Runs)
			}
		})
	}
}
