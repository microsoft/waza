package main

import (
	"bytes"
	"context"
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
