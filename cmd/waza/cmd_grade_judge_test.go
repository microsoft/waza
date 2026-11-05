package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type savedEvidenceJudge func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error)

func (f savedEvidenceJudge) Execute(_ context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return f(request)
}

func savedEvidenceFixture(t *testing.T) (string, string, *models.EvaluationOutcome) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "eval.yaml"), []byte(`name: evidence
skill: example
config: {executor: copilot-sdk, model: actor-model, judge_model: judge-model, timeout_seconds: 30, trials_per_task: 1}
tasks: [task.yaml]
graders:
  - name: judge
    type: prompt
    config:
      prompt: Judge the recorded execution, not a newly edited task. Call exactly one grade tool.
      evidence_context: {}
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "task.yaml"), []byte("id: task\nname: Recorded task\ninputs: {prompt: NEWLY_EDITED_TASK_CANARY}\n"), 0600))
	origin := models.EvidenceOrigin{EvalID: "source-evaluation", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"}
	bundle := judgeevidence.Capture(judgeevidence.CaptureInput{Origin: origin, Task: "RECORDED_TASK_CANARY",
		Output: "", OutputPresent: true, ExecutionMode: "mock",
		Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: "RECORDED_TASK_CANARY"}},
			{Data: &copilot.AssistantMessageData{Content: ""}},
		}})
	require.NoError(t, judgeevidence.Verify(bundle, origin))
	credits := 7.0
	original := &models.EvaluationOutcome{RunID: origin.EvalID,
		EvaluationUsage: &models.EvaluationUsage{Sessions: []models.EvaluationSessionUsage{{
			SessionID: "ACTOR_SESSION_CANARY", Usage: &models.UsageStats{InputTokens: 123, AICredits: &credits},
		}}},
		TestOutcomes: []models.TestOutcome{{TestID: "task", DisplayName: "Recorded task", Status: models.StatusPassed,
			Runs: []models.RunResult{{RunNumber: 1, Attempts: 1, Prompt: "RECORDED_TASK_CANARY",
				Status: models.StatusPassed, JudgeEvidence: bundle, SnapshotPath: "/DO_NOT_FOLLOW_SNAPSHOT_CANARY",
				SessionDigest: models.SessionDigest{SessionID: "ACTOR_SESSION_CANARY", Usage: &models.UsageStats{InputTokens: 123, AICredits: &credits}},
			}}}}}
	results := filepath.Join(root, "results.json")
	return filepath.Join(root, "eval.yaml"), results, original
}

func TestEvidenceSavedGradeUsesRecordedContextWithoutWorkspaceAndKeepsBillingSeparate(t *testing.T) {
	spec, results, original := savedEvidenceFixture(t)
	data, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results, data, 0600))
	output := filepath.Join(t.TempDir(), "graded.json")
	calls := 0
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		calls++
		require.Empty(t, request.SessionID)
		require.Empty(t, request.WorkspaceDir)
		require.Contains(t, request.Message, "RECORDED_TASK_CANARY")
		for _, canary := range []string{"NEWLY_EDITED_TASK_CANARY", "ACTOR_SESSION_CANARY", "DO_NOT_FOLLOW_SNAPSHOT_CANARY"} {
			require.NotContains(t, request.Message, canary)
		}
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		zero := 0.0
		return &execution.ExecutionResponse{Success: true, SessionID: fmt.Sprintf("new-judge-%d", calls),
			ModelID: request.ModelID, Usage: &models.UsageStats{InputTokens: 4, AICredits: &zero}}, err
	})
	var stdout bytes.Buffer
	err = runGradeWithJudge(t.Context(), &stdout, io.Discard, spec, "task", results,
		filepath.Join(t.TempDir(), "NONEXISTENT_WORKSPACE_CANARY"), "", output, false, "", judge)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Contains(t, stdout.String(), `"status": "passed"`)
	saved, err := os.ReadFile(output)
	require.NoError(t, err)
	graded, err := models.ParseEvaluationOutcome(saved, output)
	require.NoError(t, err)
	require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
	require.Equal(t, original.TestOutcomes[0].Runs[0].SessionDigest, graded.TestOutcomes[0].Runs[0].SessionDigest)
	require.Len(t, graded.RegradeUsage.Sessions, 1)
	require.Equal(t, 0.0, *graded.RegradeUsage.Sessions[0].Usage.AICredits)
	observation := graded.TestOutcomes[0].Runs[0].PromptJudging[0]
	require.Equal(t, "regrade", observation.Purpose)
	require.True(t, observation.SubmissionObserved)
	require.Equal(t, "new-judge-1", observation.SessionID)
	require.Equal(t, original.TestOutcomes[0].Runs[0].JudgeEvidence.SHA256, graded.TestOutcomes[0].Runs[0].JudgeEvidence.SHA256)
}

func TestEvidenceSavedGradeKeepsRecoveryEvidenceVisibleToInlineGrader(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded=%t", recorded), func(t *testing.T) {
			spec, results, original := savedEvidenceFixture(t)
			specData, err := os.ReadFile(spec)
			require.NoError(t, err)
			specData = append(specData, []byte(`
  - name: cli-evidence
    type: code
    config:
      language: javascript
      assertions:
        - "command_invocations.length === 2 && command_invocations[0].exit_code === undefined && command_invocations[1].exit_code === 0"
`)...)
			require.NoError(t, os.WriteFile(spec, specData, 0600))
			run := &original.TestOutcomes[0].Runs[0]
			run.DependencyModes = []models.DependencyMode{{Kind: "cli", Name: "az", Mode: "mocked"}}
			if recorded {
				run.CommandInvocations = []models.CommandInvocation{
					{Command: "az", Args: []string{"account"}, ExitCode: nil},
					{Command: "az", Args: []string{"account"}, ExitCode: new(0)},
				}
			}
			require.NoError(t, saveOutcome(original, results))
			output := filepath.Join(t.TempDir(), "graded.json")
			calls := 0
			err = runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", results,
				t.TempDir(), "", output, false, "", successfulSavedEvidenceJudge(t, &calls))
			require.NoError(t, err)
			graded, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.Len(t, graded.TestOutcomes, 1)
			require.Len(t, graded.TestOutcomes[0].Runs, 1)
			result := graded.TestOutcomes[0].Runs[0]
			require.Equal(t, recorded, result.Validations["cli-evidence"].Passed)
			require.Equal(t, recorded, result.Status == models.StatusPassed)
			require.Equal(t, 1, calls)
			require.Len(t, result.PromptJudging, 1)
			require.True(t, result.PromptJudging[0].SubmissionObserved)
			require.Equal(t, run.CommandInvocations, result.CommandInvocations)
			require.Equal(t, run.DependencyModes, result.DependencyModes)
		})
	}
}

func TestEvidenceSavedGradeVerboseWriteFailurePreservesJudgedRun(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		verbose, wantError        bool
		failAt, calls, judgeCalls int
	}{
		{"grader-result write failure", true, true, 2, 2, 1},
		{"initial write failure", true, true, 1, 1, 0},
		{"verbose success", true, false, 3, 2, 1},
		{"verbose disabled", false, false, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, source, original := savedEvidenceFixture(t)
			require.NoError(t, saveOutcome(original, source))
			output := filepath.Join(t.TempDir(), "graded.json")
			calls := 0
			var summary, stderr bytes.Buffer
			writer := &preflightFailWriter{failAt: tc.failAt}
			err := runGradeWithJudge(t.Context(), &summary, io.MultiWriter(&stderr, writer),
				spec, "task", source, "", "", output, tc.verbose, "", successfulSavedEvidenceJudge(t, &calls))
			if tc.wantError {
				require.ErrorContains(t, err, "failed to write verbose output: write failed")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.calls, writer.calls)
			require.Equal(t, tc.judgeCalls, calls)
			if tc.calls > 0 {
				require.Contains(t, stderr.String(), "grading task:")
			}
			if tc.calls > 1 {
				require.Contains(t, stderr.String(), "  grader judge:")
			}
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			graded, err := models.ParseEvaluationOutcome(data, output)
			require.NoError(t, err)
			require.Len(t, graded.TestOutcomes, 1)
			require.Len(t, graded.TestOutcomes[0].Runs, 1)
			run := graded.TestOutcomes[0].Runs[0]
			require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
			require.Equal(t, original.TestOutcomes[0].Runs[0].SessionDigest, run.SessionDigest)
			require.NotNil(t, graded.RegradeUsage)
			require.Len(t, graded.RegradeUsage.Sessions, tc.judgeCalls)
			require.Len(t, run.PromptJudging, tc.judgeCalls)
			if tc.judgeCalls > 0 {
				require.True(t, run.PromptJudging[0].SubmissionObserved)
				require.Equal(t, "judge-1", run.PromptJudging[0].SessionID)
				require.Equal(t, 0.25, *graded.RegradeUsage.Sessions[0].Usage.AICredits)
				if tc.wantError {
					require.Equal(t, models.StatusError, run.Status)
					require.Contains(t, run.ErrorMsg, "failed to write verbose output")
					require.Empty(t, run.Validations)
				} else {
					require.Equal(t, models.StatusPassed, run.Status)
					require.Contains(t, run.Validations, "judge")
				}
			}
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(summary.Bytes(), &fields))
			if tc.wantError {
				require.Equal(t, models.StatusError, graded.TestOutcomes[0].Status)
				require.JSONEq(t, `"error"`, string(fields["status"]))
				require.NotContains(t, fields, "overall_score")
			} else {
				require.Equal(t, models.StatusPassed, graded.TestOutcomes[0].Status)
				require.JSONEq(t, `"passed"`, string(fields["status"]))
				require.JSONEq(t, `1`, string(fields["overall_score"]))
			}
		})
	}
}

func TestEvidenceSavedGradeOperationalFailuresSaveDiagnosticsBeforeReturningError(t *testing.T) {
	for _, problem := range []string{"missing", "unbound", "cached", "new-file", "judge-failure", "ordinary-fail"} {
		t.Run(problem, func(t *testing.T) {
			spec, results, original := savedEvidenceFixture(t)
			switch problem {
			case "missing":
				original.TestOutcomes[0].Runs[0].JudgeEvidence = nil
			case "unbound":
				original.RunID = "other"
			case "cached":
				original.TestOutcomes[0].Cached = true
			case "new-file":
				data, err := os.ReadFile(spec)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(spec, bytes.Replace(data, []byte("evidence_context: {}"), []byte("evidence_context: {files: [new.txt]}"), 1), 0600))
			}
			data, err := json.Marshal(original)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(results, data, 0600))
			output := filepath.Join(t.TempDir(), "failed.json")
			calls := 0
			judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				index := 0
				if problem == "ordinary-fail" {
					index = 1
				}
				_, err := request.Tools[index].Handler(copilot.ToolInvocation{ToolName: request.Tools[index].Name})
				require.NoError(t, err)
				response := &execution.ExecutionResponse{Success: true, SessionID: "failed-judge"}
				if problem == "judge-failure" {
					return response, errors.New("simulated submission failure")
				}
				return response, nil
			})
			var stdout bytes.Buffer
			err = runGradeWithJudge(t.Context(), &stdout, io.Discard, spec, "task", results, "/NONEXISTENT_WORKSPACE_CANARY", "", output, false, "", judge)
			if problem == "ordinary-fail" {
				require.NoError(t, err, "ordinary failed judgments retain grade's existing exit policy")
				require.Contains(t, stdout.String(), `"status": "failed"`)
				require.Contains(t, stdout.String(), `"overall_score": 0`)
			} else {
				require.Error(t, err)
				require.Contains(t, stdout.String(), `"status": "error"`)
				require.NotContains(t, stdout.String(), `"overall_score"`, "an operational failure is not a zero-score judgment")
			}
			saved, err := os.ReadFile(output)
			require.NoError(t, err)
			graded, err := models.ParseEvaluationOutcome(saved, output)
			require.NoError(t, err)
			require.Len(t, graded.TestOutcomes[0].Runs, 1)
			run := graded.TestOutcomes[0].Runs[0]
			require.NotEmpty(t, run.PromptJudging)
			if strings.HasPrefix(problem, "judge-") || problem == "ordinary-fail" {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
			}
			if problem != "ordinary-fail" {
				require.Equal(t, models.StatusError, run.Status)
				require.Equal(t, "failed", run.PromptJudging[len(run.PromptJudging)-1].State)
			}
			if problem == "judge-failure" {
				// A prior judge failure must not prevent a fresh judgment of the same actor evidence.
				stdout.Reset()
				_, err = gradeRunWithJudge(t.Context(), &models.EvalSpec{Graders: []models.GraderConfig{{
					Identifier: "judge", Kind: models.GraderKindPrompt, Parameters: models.PromptGraderParameters{
						Prompt: "Judge.", EvidenceContext: &models.PromptEvidenceContext{},
					},
				}}}, &models.TestCase{TestID: "task"}, &run, "", "", io.Discard, false,
					evidenceGradeSource{EvalID: original.RunID, Executor: savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
						return &execution.ExecutionResponse{Success: true, SessionID: "retry-judge"}, err
					})})
				require.NoError(t, err)
			}
		})
	}
}

func successfulSavedEvidenceJudge(t *testing.T, calls *int) savedEvidenceJudge {
	t.Helper()
	return func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		*calls++
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		credits := 0.25
		return &execution.ExecutionResponse{Success: true, SessionID: fmt.Sprintf("judge-%d", *calls),
			ModelID: request.ModelID, Usage: &models.UsageStats{InputTokens: 4, AICredits: &credits}}, err
	}
}

func TestEvidenceSavedGradePreservesCachedSourceAcrossReload(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cached, bundle bool
	}{
		{"cached bundle", true, true},
		{"cached diagnostics", true, false},
		{"uncached control", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, source, original := savedEvidenceFixture(t)
			original.TestOutcomes[0].Cached = tc.cached
			require.Nil(t, original.TestOutcomes[0].Runs[0].Evidence)
			if !tc.bundle {
				original.TestOutcomes[0].Runs[0].JudgeEvidence = nil
			}
			require.NoError(t, saveOutcome(original, source))
			calls := 0
			judge := successfulSavedEvidenceJudge(t, &calls)
			for attempt := range 2 {
				output := filepath.Join(t.TempDir(), "graded.json")
				err := runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", source, "", "", output, false, "", judge)
				if tc.cached {
					require.ErrorContains(t, err, "cached")
					require.Zero(t, calls)
				} else {
					require.NoError(t, err)
					require.Equal(t, attempt+1, calls)
				}
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				saved, err := models.ParseEvaluationOutcome(data, output)
				require.NoError(t, err)
				require.Equal(t, tc.cached, saved.TestOutcomes[0].Cached)
				if tc.cached {
					require.Equal(t, models.StatusError, saved.TestOutcomes[0].Status)
					require.NotEmpty(t, saved.TestOutcomes[0].Runs[0].PromptJudging)
				}
				source = output
			}
		})
	}
}

func TestEvidenceSavedGradeDeterministicRegradeClearsCurrentUsage(t *testing.T) {
	spec, source, original := savedEvidenceFixture(t)
	require.NoError(t, saveOutcome(original, source))
	first := filepath.Join(t.TempDir(), "judged.json")
	calls := 0
	judge := successfulSavedEvidenceJudge(t, &calls)
	require.NoError(t, runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", source, "", "", first, false, "", judge))
	data, err := os.ReadFile(first)
	require.NoError(t, err)
	judged, err := models.ParseEvaluationOutcome(data, first)
	require.NoError(t, err)
	require.NotNil(t, judged.RegradeUsage)
	require.Len(t, judged.RegradeUsage.Sessions, 1)
	require.Equal(t, 0.25, *judged.RegradeUsage.Sessions[0].Usage.AICredits)

	config, err := os.ReadFile(spec)
	require.NoError(t, err)
	head, _, found := strings.Cut(string(config), "graders:\n")
	require.True(t, found)
	require.NoError(t, os.WriteFile(spec, []byte(head+`graders:
  - name: deterministic
    type: text
    config: {regex_not_match: [FATAL_ERROR_CANARY]}
`), 0600))
	second := filepath.Join(t.TempDir(), "deterministic.json")
	require.NoError(t, runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", first, filepath.Dir(spec), "", second, false, "", judge))
	data, err = os.ReadFile(second)
	require.NoError(t, err)
	deterministic, err := models.ParseEvaluationOutcome(data, second)
	require.NoError(t, err)
	require.Equal(t, models.StatusPassed, deterministic.TestOutcomes[0].Status)
	require.Contains(t, deterministic.TestOutcomes[0].Runs[0].Validations, "deterministic")
	require.Equal(t, 1, calls, "deterministic grading must not start another judge")
	require.Nil(t, deterministic.RegradeUsage)
	require.NotContains(t, string(data), `"regrade_usage"`)
	require.Equal(t, original.EvaluationUsage, deterministic.EvaluationUsage)
	require.Equal(t, original.TestOutcomes[0].Runs[0].SessionDigest, deterministic.TestOutcomes[0].Runs[0].SessionDigest)
}

func TestEvidenceSavedGradeRejectsCrossGraderSelectionConflicts(t *testing.T) {
	for _, excluded := range []string{"a.txt", "a", "a.txt-sibling"} {
		for _, scope := range []string{"eval", "task"} {
			t.Run(excluded+"/"+scope, func(t *testing.T) {
				spec, source, original := savedEvidenceFixture(t)
				file := "a.txt"
				if excluded == "a" {
					file = "a/report.txt"
				}
				config, err := os.ReadFile(spec)
				require.NoError(t, err)
				config = []byte(strings.Replace(string(config), "evidence_context: {}", "evidence_context: {files: ["+file+"]}", 1))
				exclusion := "\n  - name: excluded\n    type: prompt\n    config:\n      prompt: Judge without evaluator inputs.\n      evidence_context: {evaluator_only: [" + excluded + "]}\n"
				if scope == "task" {
					taskPath := filepath.Join(filepath.Dir(spec), "task.yaml")
					task, err := os.ReadFile(taskPath)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(taskPath, append(task, []byte("graders:"+exclusion)...), 0600))
				} else {
					config = append(config, []byte(exclusion)...)
				}
				require.NoError(t, os.WriteFile(spec, config, 0600))
				old := original.TestOutcomes[0].Runs[0].JudgeEvidence
				content := "RECORDED_FILE_CANARY"
				bundle := judgeevidence.Capture(judgeevidence.CaptureInput{
					Origin: old.Origin, Task: *old.Task.Items, Output: *old.Output.Items, OutputPresent: true, ExecutionMode: "mock",
					Events: []copilot.SessionEvent{
						{Data: &copilot.UserMessageData{Content: *old.Task.Items}},
						{Data: &copilot.AssistantMessageData{Content: *old.Output.Items}},
					},
					Files: &models.JudgeFileCapture{Files: []models.JudgeEvidenceFile{{
						Path: file, Content: content, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content))),
					}}},
					Selection: models.PromptEvidenceContext{Files: []string{file}},
				})
				require.NoError(t, judgeevidence.Verify(bundle, old.Origin))
				original.TestOutcomes[0].Runs[0].JudgeEvidence = bundle
				require.NoError(t, saveOutcome(original, source))
				calls := 0
				output := filepath.Join(t.TempDir(), "graded.json")
				err = runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", source, "", "", output, false, "", successfulSavedEvidenceJudge(t, &calls))
				conflict := excluded != "a.txt-sibling"
				if conflict {
					require.ErrorContains(t, err, "conflict")
					require.Zero(t, calls)
				} else {
					require.NoError(t, err)
					require.Equal(t, 2, calls)
				}
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				graded, err := models.ParseEvaluationOutcome(data, output)
				require.NoError(t, err)
				require.NotNil(t, graded.RegradeUsage)
				if conflict {
					require.Equal(t, models.StatusError, graded.TestOutcomes[0].Status)
					require.Empty(t, graded.RegradeUsage.Sessions)
				} else {
					require.Equal(t, models.StatusPassed, graded.TestOutcomes[0].Status)
					require.Len(t, graded.RegradeUsage.Sessions, 2)
				}
			})
		}
	}
}

func mixedEvidenceFixture(t *testing.T) (string, string, *models.EvaluationOutcome) {
	t.Helper()
	spec, source, original := savedEvidenceFixture(t)
	config, err := os.ReadFile(spec)
	require.NoError(t, err)
	config = []byte(strings.Replace(string(config), "tasks: [task.yaml]", "tasks: [task.yaml, zz-broken.yaml, zzz-evidence.yaml]", 1))
	head, gradersYAML, found := strings.Cut(string(config), "graders:\n")
	require.True(t, found)
	require.NoError(t, os.WriteFile(spec, []byte(head), 0600))
	taskPath := filepath.Join(filepath.Dir(spec), "task.yaml")
	task, err := os.ReadFile(taskPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(taskPath, append(task, []byte("graders:\n"+gradersYAML)...), 0600))
	thirdTask := strings.Replace(string(task), "id: task", "id: zzz-evidence", 1) + "graders:\n" + gradersYAML
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(spec), "zzz-evidence.yaml"), []byte(thirdTask), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(spec), "zz-broken.yaml"), []byte(`id: zz-broken
name: ZZ missing schema
inputs: {prompt: Mechanical task}
graders:
  - name: missing_schema
    type: json_schema
    config: {schema_file: missing-schema.json}
`), 0600))
	original.TestOutcomes = append(original.TestOutcomes, models.TestOutcome{TestID: "zz-broken", DisplayName: "ZZ missing schema",
		Status: models.StatusPassed, Runs: []models.RunResult{{RunNumber: 1, Attempts: 1, Status: models.StatusPassed, FinalOutput: "{}"}}})
	third := original.TestOutcomes[0]
	third.TestID = "zzz-evidence"
	third.Runs = append([]models.RunResult(nil), third.Runs...)
	old := third.Runs[0].JudgeEvidence
	origin := old.Origin
	origin.TaskID = third.TestID
	third.Runs[0].JudgeEvidence = judgeevidence.Capture(judgeevidence.CaptureInput{
		Origin: origin, Task: *old.Task.Items, Output: *old.Output.Items, OutputPresent: true, ExecutionMode: "mock",
		Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: *old.Task.Items}},
			{Data: &copilot.AssistantMessageData{Content: *old.Output.Items}},
		},
	})
	require.NoError(t, judgeevidence.Verify(third.Runs[0].JudgeEvidence, origin))
	original.TestOutcomes = append(original.TestOutcomes, third)
	require.NoError(t, saveOutcome(original, source))
	return spec, source, original
}

func TestEvidenceSavedGradeMixedTaskFailurePreservesEarlierUsage(t *testing.T) {
	spec, source, original := mixedEvidenceFixture(t)
	calls := 0
	output := filepath.Join(t.TempDir(), "graded.json")
	var summary bytes.Buffer
	err := runGradeWithJudge(t.Context(), &summary, io.Discard, spec, "", source, filepath.Dir(spec), "", output, false, "", successfulSavedEvidenceJudge(t, &calls))
	require.ErrorContains(t, err, "schema")
	require.Equal(t, 1, calls, "the evidence task must incur usage before the mechanical task fails")
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	graded, err := models.ParseEvaluationOutcome(data, output)
	require.NoError(t, err)
	require.NotNil(t, graded.RegradeUsage)
	require.Len(t, graded.RegradeUsage.Sessions, 1)
	require.Equal(t, 0.25, *graded.RegradeUsage.Sessions[0].Usage.AICredits)
	require.Len(t, graded.TestOutcomes[0].Runs[0].PromptJudging, 1)
	require.True(t, graded.TestOutcomes[0].Runs[0].PromptJudging[0].SubmissionObserved)
	require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
	require.Equal(t, models.StatusError, graded.TestOutcomes[1].Status)
	require.Equal(t, models.StatusError, graded.TestOutcomes[1].Runs[0].Status)
	require.Contains(t, graded.TestOutcomes[1].Runs[0].ErrorMsg, "missing-schema.json")
	require.Equal(t, original.TestOutcomes[2].Status, graded.TestOutcomes[2].Status)
	require.Equal(t, original.TestOutcomes[2].Runs, graded.TestOutcomes[2].Runs, "the later judge must remain unattempted and retain its original runs")
	require.Contains(t, summary.String(), `"status": "error"`)
	var summaryFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(summary.Bytes(), &summaryFields))
	require.NotContains(t, summaryFields, "overall_score")
}

func TestEvidenceSavedGradeMechanicalErrorBeforeEvidenceRemainsFailFast(t *testing.T) {
	spec, source, original := mixedEvidenceFixture(t)
	root := filepath.Dir(spec)
	broken, err := os.ReadFile(filepath.Join(root, "zz-broken.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "aa-broken.yaml"), []byte(strings.Replace(string(broken), "id: zz-broken", "id: aa-broken", 1)), 0600))
	config, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(spec, []byte(strings.Replace(string(config), "zz-broken.yaml", "aa-broken.yaml", 1)), 0600))
	original.TestOutcomes[1].TestID = "aa-broken"
	require.NoError(t, saveOutcome(original, source))
	parsed, err := models.LoadEvalSpec(spec)
	require.NoError(t, err)
	tasks, err := loadGradeTasks(parsed, spec, "")
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	require.Equal(t, "aa-broken", tasks[0].TestID, "the mechanical error must precede both model-backed tasks")
	calls := 0
	output := filepath.Join(t.TempDir(), "graded.json")
	var summary bytes.Buffer
	err = runGradeWithJudge(t.Context(), &summary, io.Discard, spec, "", source, root, "", output, false, "", successfulSavedEvidenceJudge(t, &calls))
	require.ErrorContains(t, err, "schema")
	require.Zero(t, calls, "a known fatal mechanical error must not start a later paid judge")
	require.Empty(t, summary.String())
	require.NoFileExists(t, output)
}

func TestEvidenceSavedGradeNearLimitPrettyJSONRoundTrip(t *testing.T) {
	spec, source, original := savedEvidenceFixture(t)
	origin := original.TestOutcomes[0].Runs[0].JudgeEvidence.Origin
	events := []copilot.SessionEvent{{Data: &copilot.UserMessageData{Content: "RECORDED_TASK_CANARY"}}}
	for range 1800 {
		events = append(events, copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: strings.Repeat("m", 70)}})
	}
	bundle := judgeevidence.Capture(judgeevidence.CaptureInput{
		Origin: origin, Task: "RECORDED_TASK_CANARY", Output: strings.Repeat("m", 70),
		OutputPresent: true, ExecutionMode: "mock", Events: events,
	})
	require.NoError(t, judgeevidence.Verify(bundle, origin))
	compact, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.LessOrEqual(t, len(compact), models.JudgeEvidenceMaxBytes)
	original.TestOutcomes[0].Runs[0].JudgeEvidence = bundle
	require.NoError(t, saveOutcome(original, source))
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	var wire struct {
		Tasks []struct {
			Runs []struct {
				JudgeEvidence json.RawMessage `json:"judge_evidence"`
			} `json:"runs"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	require.Len(t, wire.Tasks, 1)
	require.Len(t, wire.Tasks[0].Runs, 1)
	require.Greater(t, len(wire.Tasks[0].Runs[0].JudgeEvidence), models.JudgeEvidenceMaxBytes, "fixture must exercise indentation beyond the compact limit")
	parsed, err := models.ParseEvaluationOutcome(data, source)
	require.NoError(t, err)
	require.NoError(t, judgeevidence.Verify(parsed.TestOutcomes[0].Runs[0].JudgeEvidence, origin))
	require.Equal(t, bundle.SHA256, parsed.TestOutcomes[0].Runs[0].JudgeEvidence.SHA256)
	calls := 0
	output := filepath.Join(t.TempDir(), "regraded.json")
	require.NoError(t, runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", source, "", "", output, false, "", successfulSavedEvidenceJudge(t, &calls)))
	require.Equal(t, 1, calls)
	data, err = os.ReadFile(output)
	require.NoError(t, err)
	regraded, err := models.ParseEvaluationOutcome(data, output)
	require.NoError(t, err)
	require.Equal(t, bundle.SHA256, regraded.TestOutcomes[0].Runs[0].JudgeEvidence.SHA256)
}
