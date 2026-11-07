package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/reporting"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func classificationRuntimeGuard(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "guard.go")
	require.NoError(t, os.WriteFile(source, []byte(`package main
import "os"
func main() {
	path, err := os.Executable()
	if err != nil { os.Exit(65) }
	if err := os.WriteFile(path+".invoked", nil, 0600); err != nil { os.Exit(74) }
	os.Exit(64)
}
`), 0600))
	path := filepath.Join(dir, "reject-copilot")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	log, err := exec.CommandContext(t.Context(), "go", "build", "-o", path, source).CombinedOutput()
	require.NoError(t, err, "%s", log)
	err = exec.CommandContext(t.Context(), path).Run()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 64, exit.ExitCode())
	require.FileExists(t, path+".invoked")
	require.NoError(t, os.Remove(path+".invoked"))
	return path
}

func TestJudgeErrorClassificationCLIProcess(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	guard := classificationRuntimeGuard(t)
	for _, control := range []struct {
		name, grader string
		status       models.Status
		exit         int
	}{
		{"grader operational error", `type: file
    config: {must_exist: [../outside-workspace]}`, models.StatusError, 2},
		{"completed false verdict", `type: text
    config: {contains: [IMPOSSIBLE_OUTPUT_CANARY]}`, models.StatusFailed, 1},
		{"completed true verdict", `type: text
    config: {contains: [CAPTURED_OUTPUT_CANARY]}`, models.StatusPassed, 0},
	} {
		t.Run(control.name, func(t *testing.T) {
			dir := t.TempDir()
			// Only invocation-owned cache ancestry is marked as inadmissible.
			require.NoError(t, os.Mkdir(filepath.Join(dir, "skills"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(dir, "skills", "synthetic"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "synthetic", "SKILL.md"),
				[]byte("---\nname: synthetic\ndescription: Synthetic classification fixture.\n---\nReturn the requested answer.\n"), 0600))
			spec := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(spec, []byte(`name: classification
skill: synthetic
config: {executor: mock, model: mock-model, timeout_seconds: 30, trials_per_task: 2, max_attempts: 2, skill_directories: [skills/synthetic]}
tasks: [task.yaml]
graders:
  - name: verdict
    `+control.grader+"\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
				[]byte("id: task\nname: Classification task\ninputs: {prompt: CAPTURED_OUTPUT_CANARY}\n"), 0600))
			output := filepath.Join(dir, "results.json")
			command := exec.CommandContext(t.Context(), binary, "--no-update-check", "run", spec, "--output", output)
			command.Dir = dir
			command.Env = append(os.Environ(), "NO_COLOR=1", "ENABLE_COPILOT_TESTS=false",
				"COPILOT_BASE_URL=", "COPILOT_PROVIDER_BASE_URL=",
				"COPILOT_GITHUB_TOKEN=offline-synthetic-token", "COPILOT_CLI_PATH="+guard,
				"HOME="+dir, "USERPROFILE="+dir, "XDG_CACHE_HOME="+dir, "LOCALAPPDATA="+dir)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "stdout=%s stderr=%s", stdout.String(), stderr.String())
				code = exit.ExitCode()
			}
			require.Equal(t, control.exit, code, "stdout=%s stderr=%s", stdout.String(), stderr.String())
			require.NoFileExists(t, guard+".invoked", "no provider/runtime execution is permitted")
			outcome, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err, "stdout=%s stderr=%s", stdout.String(), stderr.String())
			require.Len(t, outcome.TestOutcomes, 1)
			task := outcome.TestOutcomes[0]
			require.Equal(t, control.status, task.Status)
			require.Len(t, task.Runs, 2)
			require.Equal(t, 1, outcome.Digest.TotalTests)
			require.Equal(t, 1, outcome.Digest.Succeeded+outcome.Digest.Failed+outcome.Digest.Errors+outcome.Digest.Skipped)
			for _, run := range task.Runs {
				require.Equal(t, control.status, run.Status)
				require.Contains(t, run.FinalOutput, "CAPTURED_OUTPUT_CANARY")
				if control.status == models.StatusError {
					require.Equal(t, 1, run.Attempts, "operational errors must not be retried")
					require.Empty(t, run.Validations)
					require.Contains(t, run.ErrorMsg, "running graders:")
				} else {
					require.Empty(t, run.ErrorMsg)
					require.Len(t, run.Validations, 1)
					require.Equal(t, control.status == models.StatusPassed, run.Validations["verdict"].Passed)
				}
			}
			switch control.status {
			case models.StatusError:
				require.Equal(t, 1, outcome.Digest.Errors)
				require.Zero(t, outcome.Digest.Failed)
				require.Equal(t, 2, task.Stats.ErrorRuns)
				require.Contains(t, stderr.String(), "0 failed and 1 error(s)")
			case models.StatusFailed:
				require.Equal(t, 1, outcome.Digest.Failed)
				require.Zero(t, outcome.Digest.Errors)
				require.Equal(t, 2, task.Stats.FailedRuns)
				require.Contains(t, stderr.String(), "1 failed and 0 error(s)")
			case models.StatusPassed:
				require.Equal(t, 1, outcome.Digest.Succeeded)
				require.Zero(t, outcome.Digest.Errors)
			}
			assertClassificationConsumers(t, dir, outcome)
			if control.status == models.StatusError {
				t.Run("grade preserves operational exit", func(t *testing.T) {
					regradedPath := filepath.Join(t.TempDir(), "regraded.json")
					cmd := exec.CommandContext(t.Context(), binary, "--no-update-check", "grade", spec,
						"--results", output, "--workspace", dir, "--output", regradedPath)
					cmd.Dir, cmd.Env = command.Dir, command.Env
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					err := cmd.Run()
					var exit *exec.ExitError
					require.ErrorAs(t, err, &exit, "stdout=%s stderr=%s", stdout.String(), stderr.String())
					require.Equal(t, 2, exit.ExitCode())
					var report map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
					require.JSONEq(t, `"error"`, string(report["status"]))
					require.NotContains(t, report, "overall_score")
					regraded, err := models.LoadEvaluationOutcome(regradedPath)
					require.NoError(t, err)
					require.Equal(t, 1, regraded.Digest.Errors)
					require.Zero(t, regraded.Digest.Failed)
					require.NoFileExists(t, guard+".invoked")
				})
				for _, mode := range []struct {
					name string
					args []string
					want int
				}{
					{"baseline", []string{"--baseline"}, 1},
					{"models", []string{"--model", "first-model", "--model", "last-model"}, 2},
				} {
					t.Run(mode.name, func(t *testing.T) {
						outputDir := t.TempDir()
						args := append([]string{"--no-update-check", "run", spec, "--output-dir", outputDir}, mode.args...)
						cmd := exec.CommandContext(t.Context(), binary, args...)
						cmd.Dir, cmd.Env = command.Dir, command.Env
						log, err := cmd.CombinedOutput()
						var exit *exec.ExitError
						require.ErrorAs(t, err, &exit, "%s", log)
						require.Equal(t, 2, exit.ExitCode(), "%s", log)
						final, partial := outcomeJSONFiles(t, outputDir)
						require.Empty(t, partial)
						require.Len(t, final, mode.want)
						for _, path := range final {
							saved, err := models.LoadEvaluationOutcome(path)
							require.NoError(t, err)
							require.Equal(t, 1, saved.Digest.Errors)
							if mode.name == "baseline" {
								require.True(t, saved.IsBaseline)
								require.NotNil(t, saved.BaselineOutcome)
								require.Equal(t, 1, saved.BaselineOutcome.Digest.Errors)
							}
						}
						require.NoFileExists(t, guard+".invoked")
					})
				}
			}
		})
	}
}

func TestFrozenJudgeSetupErrorCLIClassification(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
		require.Equal(t, "copilot-sdk", cfg.EngineType)
		client := &copilotfake.Client{Send: func(_ *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
			return []copilot.SessionEvent{
				{Data: &copilot.UserMessageData{Content: options.Prompt}},
				{Data: &copilot.AssistantMessageData{Content: "CAPTURED_OUTPUT_CANARY"}},
			}, nil
		}}
		return client.Engine(), nil
	}
	guard := classificationRuntimeGuard(t)
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "skills"), 0700))
	for name, value := range map[string]string{
		"COPILOT_BASE_URL": "", "COPILOT_PROVIDER_BASE_URL": "",
		"COPILOT_GITHUB_TOKEN": "offline-synthetic-token", "COPILOT_CLI_PATH": guard,
		"HOME": dir, "USERPROFILE": dir, "XDG_CACHE_HOME": dir, "LOCALAPPDATA": dir,
	} {
		t.Setenv(name, value)
	}
	spec := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(`name: classification
skill: synthetic
config: {executor: copilot-sdk, model: mock-model, timeout_seconds: 30, trials_per_task: 2, max_attempts: 2}
tasks: [task.yaml]
graders:
  - name: verdict
    type: prompt
    config:
      prompt: Call exactly one grade tool.
      evidence_context: {}
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte("id: task\nname: Classification task\ninputs: {prompt: CAPTURED_OUTPUT_CANARY}\n"), 0600))
	output := filepath.Join(dir, "results.json")
	command := newRunCommand()
	command.SetArgs([]string{spec, "--output", output})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	var benchmark *BenchmarkError
	require.ErrorAs(t, err, &benchmark, "stdout=%s stderr=%s", stdout.String(), stderr.String())
	require.Contains(t, err.Error(), "0 failed and 1 error(s)")
	require.NoFileExists(t, guard+".invoked")
	outcome, err := models.LoadEvaluationOutcome(output)
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 1)
	task := outcome.TestOutcomes[0]
	require.Equal(t, models.StatusError, task.Status)
	require.Equal(t, 1, outcome.Digest.Errors)
	require.Zero(t, outcome.Digest.Failed)
	require.Len(t, task.Runs, 2)
	for _, run := range task.Runs {
		require.Equal(t, models.StatusError, run.Status)
		require.Equal(t, 1, run.Attempts)
		require.Contains(t, run.FinalOutput, "CAPTURED_OUTPUT_CANARY")
		require.Contains(t, run.ErrorMsg, "creating isolated evidence judge directories:")
		require.Empty(t, run.Validations)
		require.NoError(t, judgeevidence.Verify(run.JudgeEvidence, run.JudgeEvidence.Origin))
		require.Len(t, run.PromptJudging, 1)
		require.Equal(t, "failed", run.PromptJudging[0].State)
		require.False(t, run.PromptJudging[0].SubmissionObserved)
	}
	assertClassificationConsumers(t, dir, outcome)
	assertClassificationRegradeRecovery(t, spec, output, outcome)
}

func assertClassificationConsumers(t *testing.T, dir string, outcome *models.EvaluationOutcome) {
	t.Helper()
	handler := webapi.NewHandlers(webapi.NewFileStore(dir))
	request := httptest.NewRequest(http.MethodGet, "/api/runs/"+outcome.RunID, nil)
	request.SetPathValue("id", outcome.RunID)
	response := httptest.NewRecorder()
	handler.HandleRunDetail(response, request)
	require.Equal(t, http.StatusOK, response.Code, "%s", response.Body.String())
	var detail webapi.RunDetail
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Len(t, detail.Tasks, 1)
	require.Equal(t, string(outcome.TestOutcomes[0].Status), detail.Tasks[0].Outcome)
	if outcome.TestOutcomes[0].Runs[0].JudgeEvidence != nil {
		require.Len(t, detail.Tasks[0].PromptJudgingRuns, 2)
	}

	junit := reporting.ConvertToJUnit(outcome)
	require.Equal(t, outcome.Digest.Errors, junit.Errors)
	require.Equal(t, outcome.Digest.Failed, junit.Failures)
	require.Len(t, junit.TestSuites, 1)
	require.Len(t, junit.TestSuites[0].TestCases, 1)
	require.Equal(t, outcome.Digest.Errors > 0, junit.TestSuites[0].TestCases[0].Error != nil)
	require.Equal(t, outcome.Digest.Failed > 0, junit.TestSuites[0].TestCases[0].Failure != nil)

	// Execute the real dashboard CSV exporter against the actual HTTP response.
	// Node type stripping requires no frontend packages or generated assets.
	script := `import {pathToFileURL} from "node:url";
const {exportRunDetailToCSV} = await import(pathToFileURL(process.argv[1]));
let blob;
globalThis.URL.createObjectURL = value => { blob = value; return "blob:synthetic"; };
globalThis.URL.revokeObjectURL = () => {};
globalThis.document = {createElement: () => ({click() {}})};
let input = "";
for await (const chunk of process.stdin) input += chunk;
exportRunDetailToCSV(JSON.parse(input));
process.stdout.write(await blob.text());`
	command := exec.CommandContext(t.Context(), "node", "--experimental-strip-types", "--input-type=module", "-e", script,
		testutil.RepoFile(t, "web", "src", "lib", "export.ts"))
	command.Stdin = bytes.NewReader(response.Body.Bytes())
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	require.NoError(t, command.Run(), "%s", stderr.String())
	rows, err := csv.NewReader(&stdout).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "Outcome", rows[0][1])
	require.Equal(t, "Classification task", rows[1][0])
	require.Equal(t, string(outcome.TestOutcomes[0].Status), rows[1][1])
}

func assertClassificationRegradeRecovery(t *testing.T, spec, source string, original *models.EvaluationOutcome) {
	t.Helper()
	for _, passed := range []bool{true, false} {
		t.Run(fmt.Sprintf("frozen regrade passed=%t", passed), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "regraded.json")
			calls := 0
			judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				require.Contains(t, request.Message, "CAPTURED_OUTPUT_CANARY")
				tool := request.Tools[0]
				if !passed {
					tool = request.Tools[1]
				}
				_, err := tool.Handler(copilot.ToolInvocation{ToolName: tool.Name})
				return &execution.ExecutionResponse{Success: true, SessionID: fmt.Sprintf("regrade-%d", calls)}, err
			})
			var summary bytes.Buffer
			require.NoError(t, runGradeWithJudge(t.Context(), &summary, io.Discard, spec, "task", source, "", "", output, false, "", judge))
			require.Equal(t, 2, calls)
			graded, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.Zero(t, graded.Digest.Errors)
			require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
			require.Len(t, graded.TestOutcomes, 1)
			task := graded.TestOutcomes[0]
			status := models.StatusFailed
			if passed {
				status = models.StatusPassed
			}
			require.Equal(t, status, task.Status)
			require.Len(t, task.Runs, 2)
			for i, run := range task.Runs {
				require.Equal(t, status, run.Status)
				require.Empty(t, run.ErrorMsg)
				require.Equal(t, original.TestOutcomes[0].Runs[i].SessionDigest, run.SessionDigest)
				require.Equal(t, original.TestOutcomes[0].Runs[i].JudgeEvidence, run.JudgeEvidence)
				require.Len(t, run.PromptJudging, 2)
				require.True(t, run.PromptJudging[1].SubmissionObserved)
				require.Equal(t, passed, run.Validations["verdict"].Passed)
			}
			var report models.GradeOutcome
			require.NoError(t, json.Unmarshal(summary.Bytes(), &report))
			require.Equal(t, passed, report.Passed)
			require.Equal(t, status, report.Status)
		})
	}
}

func TestRegradePreservedOperationalErrorClassification(t *testing.T) {
	dir := t.TempDir()
	spec := gradeSpec(t, dir, "name: preserved\nconfig: {executor: mock, trials_per_task: 2, timeout_seconds: 30}\ntasks: [tasks/*.yaml]\ngraders: [{name: check, type: text, config: {contains: [ready]}}]\n")
	writeTaskFile(t, dir, "task.yaml", "id: task\nname: Task\ninputs: {prompt: ready}\n")
	original := outcomeWithTasks(models.TestOutcome{TestID: "task", Status: models.StatusFailed,
		Runs: []models.RunResult{
			{RunNumber: 1, Status: models.StatusError, ErrorMsg: "recorded execution failed", FinalOutput: "partial"},
			{RunNumber: 2, Status: models.StatusPassed, FinalOutput: "ready"},
		}})
	source := gradeResultsFile(t, dir, original)
	output := filepath.Join(t.TempDir(), "graded.json")
	var summary bytes.Buffer
	require.ErrorContains(t, runGradeWithJudge(t.Context(), &summary, io.Discard, spec, "", source, dir, "", output, false, "", nil), "recorded execution errors remain")
	var report map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(summary.Bytes(), &report))
	require.JSONEq(t, `"error"`, string(report["status"]))
	require.JSONEq(t, `false`, string(report["passed"]))
	require.NotContains(t, report, "overall_score")
	var tasks map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(report["tasks"], &tasks))
	require.JSONEq(t, `"error"`, string(tasks["task"]["status"]))
	require.NotContains(t, tasks["task"], "overall_score")
	graded, err := models.LoadEvaluationOutcome(output)
	require.NoError(t, err)
	require.Equal(t, models.StatusError, graded.TestOutcomes[0].Status)
	require.Equal(t, 1, graded.Digest.Errors)
	require.Zero(t, graded.Digest.Failed)
	require.Equal(t, original.TestOutcomes[0].Runs[0].ErrorMsg, graded.TestOutcomes[0].Runs[0].ErrorMsg)
	// Reading the old artifact does not silently rewrite its historical status.
	historical, err := models.LoadEvaluationOutcome(source)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, historical.TestOutcomes[0].Status)
}

func TestCompletedBenchmarkErrorPrecedence(t *testing.T) {
	validation := &TestFailureError{Message: "failed"}
	operational := &BenchmarkError{Message: "error"}
	for _, err := range []error{validation, operational, errors.Join(validation, operational), errors.Join(operational, validation)} {
		require.True(t, completedBenchmarkError(err))
	}
	require.False(t, completedBenchmarkError(errors.New("configuration invalid")))
	require.False(t, completedBenchmarkError(nil))
}

func TestMultiModelOperationalErrorCannotBeErasedByLaterFailure(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
		client := &copilotfake.Client{Send: func(_ *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
			if cfg.ModelID == "operational-model" {
				return nil, errors.New("observed operational failure")
			}
			return []copilot.SessionEvent{
				{Data: &copilot.UserMessageData{Content: options.Prompt}},
				{Data: &copilot.AssistantMessageData{Content: "completed negative answer"}},
			}, nil
		}}
		return client.Engine(), nil
	}
	spec := createTestSpec(t, "copilot-sdk")
	data, err := os.ReadFile(spec)
	require.NoError(t, err)
	data = append(data, []byte("\ngraders: [{name: check, type: text, config: {contains: [IMPOSSIBLE_OUTPUT_CANARY]}}]\n")...)
	require.NoError(t, os.WriteFile(spec, data, 0600))
	dir := t.TempDir()
	command := newRunCommand()
	command.SetArgs([]string{spec, "--model", "operational-model", "--model", "negative-model", "--output-dir", dir})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	err = command.Execute()
	var operational *BenchmarkError
	var failure *TestFailureError
	require.ErrorAs(t, err, &operational)
	require.ErrorAs(t, err, &failure)
	final, partial := outcomeJSONFiles(t, dir)
	require.Empty(t, partial)
	require.Len(t, final, 2)
	for _, path := range final {
		outcome, err := models.LoadEvaluationOutcome(path)
		require.NoError(t, err)
		if outcome.Setup.ModelID == "operational-model" {
			require.Equal(t, 1, outcome.Digest.Errors)
			require.Zero(t, outcome.Digest.Failed)
		} else {
			require.Equal(t, "negative-model", outcome.Setup.ModelID)
			require.Zero(t, outcome.Digest.Errors)
			require.Equal(t, 1, outcome.Digest.Failed)
		}
	}
}
