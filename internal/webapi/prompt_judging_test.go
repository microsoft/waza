package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
	"github.com/stretchr/testify/require"
)

type projectionJudge struct{ calls int }

func (j *projectionJudge) Execute(_ context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	j.calls++
	index := (j.calls - 1) % 2
	_, err := request.Tools[index].Handler(copilot.ToolInvocation{ToolName: request.Tools[index].Name})
	usage := &models.UsageStats{InputTokens: 4, OutputTokens: 1}
	if index == 0 {
		zero := 0.0
		usage.AICredits = &zero
	}
	return &execution.ExecutionResponse{Success: true, SessionID: fmt.Sprintf("judge-%d", j.calls),
		ModelID: "judge-model", Usage: usage}, err
}

func promptJudgingProducer(t *testing.T) *models.EvaluationOutcome {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("producer-selected-file fixture requires Linux/macOS")
	}
	client := &copilotfake.Client{Send: func(config *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		for _, name := range []string{"a.txt", "b.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(config.WorkingDirectory, name), []byte("PRIVATE_FILE_BODY_"+name), 0600))
		}
		return []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: options.Prompt}},
			{Data: &copilot.AssistantMessageData{Content: "PRIVATE_ACTOR_OUTPUT_CANARY"}},
		}, nil
	}}
	engine := client.Engine()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
	selection := models.PromptEvidenceContext{Files: []string{"a.txt", "b.txt"}}
	response, err := engine.Execute(t.Context(), &execution.ExecutionRequest{Message: "Create reports",
		NoSkills: true, SkipWorkspaceCapture: true, CaptureJudgeFiles: func(root string) *models.JudgeFileCapture {
			return judgeevidence.CaptureFiles(root, selection)
		}})
	require.NoError(t, err)
	require.True(t, response.Success)
	origin := models.EvidenceOrigin{EvalID: "run-001", TaskID: "report-task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"}
	bundle := judgeevidence.Capture(judgeevidence.CaptureInput{Origin: origin, Task: "Create reports",
		Output: response.FinalOutput, OutputPresent: true, Events: copilotevents.ToSDK(response.Events),
		Selection: selection, Files: response.JudgeFiles, ExecutionMode: "live"})
	require.NoError(t, judgeevidence.Verify(bundle, origin))
	run := models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusFailed, JudgeEvidence: bundle}
	context := &graders.Context{JudgeEvidence: bundle, JudgeOrigin: origin, EvidenceExecutor: &projectionJudge{},
		RecordPromptJudging: func(observation models.PromptJudgeObservation) {
			// Normalize only volatile wall-clock duration for the frozen browser oracle.
			observation.DurationMs = 0
			run.PromptJudging = append(run.PromptJudging, observation)
		}}
	for _, name := range []string{"a", "b"} {
		g, err := graders.NewPromptGrader(name+"-judge", models.PromptGraderParameters{
			Prompt:          "Judge the recorded evidence. Call one grade tool.",
			EvidenceContext: &models.PromptEvidenceContext{Files: []string{name + ".txt"}},
		})
		require.NoError(t, err)
		_, err = g.Grade(t.Context(), context)
		require.NoError(t, err)
	}
	unavailableOrigin := origin
	unavailableOrigin.RunNumber = 2
	unavailable := judgeevidence.Capture(judgeevidence.CaptureInput{Origin: unavailableOrigin,
		Task: "Create reports", Output: "", OutputPresent: false, ExecutionMode: "live"})
	run2 := models.RunResult{RunNumber: 2, Attempts: 1, Status: models.StatusError, JudgeEvidence: unavailable,
		PromptJudging: []models.PromptJudgeObservation{{Grader: "a-judge", BundleSHA256: unavailable.SHA256,
			Context: "frozen_inline", State: "failed", Purpose: "run", Inspection: "not_observable",
			Diagnostics: []models.EvidenceDiagnostic{{Code: "invalid_evidence"}}}}}
	outcome := &models.EvaluationOutcome{RunID: "run-001", BenchName: "evidence-judge", Timestamp: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Setup: models.OutcomeSetup{EngineType: "copilot-sdk", ModelID: "actor-model", JudgeModel: "judge-model", RunsPerTest: 3},
		TestOutcomes: []models.TestOutcome{{TestID: "report-task", DisplayName: "Recorded reports", Status: models.StatusError,
			Runs: []models.RunResult{run, run2, {RunNumber: 3, Attempts: 1, Status: models.StatusPassed}}}}}
	zero := 0.0
	outcome.RegradeUsage = &models.EvaluationUsage{Sessions: []models.EvaluationSessionUsage{{SessionID: "regrade-session",
		Usage: &models.UsageStats{InputTokens: 1, AICredits: &zero}}}}
	return orchestration.RegradeOutcome(outcome, outcome.TestOutcomes, "judge-model", "")
}

func TestPromptJudgingProducerToResultReaderToAPIMetadata(t *testing.T) {
	outcome := promptJudgingProducer(t)
	encoded, err := json.Marshal(outcome)
	require.NoError(t, err)
	parsed, err := models.ParseEvaluationOutcome(encoded, "recorded-results.json")
	require.NoError(t, err)
	detail := outcomeToDetail(parsed)
	rows := detail.Tasks[0].PromptJudgingRuns
	require.Len(t, rows, 3)
	require.Equal(t, "recorded", rows[0].Assessment)
	require.Len(t, rows[0].Observations, 2)
	require.Equal(t, "unavailable", rows[1].Assessment)
	require.Equal(t, "failed", rows[1].Observations[0].State)
	require.Equal(t, "unassessed", rows[2].Assessment)
	require.NotEqual(t, rows[0].Observations[0].DeliverySHA256, rows[0].Observations[1].DeliverySHA256)
	require.Equal(t, 0.0, *rows[0].Observations[0].Usage.AICredits)
	require.Nil(t, rows[0].Observations[1].Usage.AICredits)
	require.Equal(t, 0.0, *detail.RegradeUsage.AICredits)
	metadata, err := json.Marshal(rows)
	require.NoError(t, err)
	for _, private := range []string{"PRIVATE_FILE_BODY", "PRIVATE_ACTOR_OUTPUT", "a.txt", "b.txt", "evidence_context", "rendered_prompt", "workspace"} {
		require.NotContains(t, string(metadata), private)
	}
	require.Equal(t, "not_observable", rows[0].Observations[0].Inspection)
	fixture, err := json.MarshalIndent(detail, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("..", "..", "web", "e2e", "fixtures", "prompt-judging.json")
	if os.Getenv("UPDATE_PROMPT_JUDGING_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, append(fixture, '\n'), 0600))
	}
	expected, err := os.ReadFile(path)
	require.NoError(t, err, "generate the producer-backed fixture with UPDATE_PROMPT_JUDGING_FIXTURE=1")
	var oracle struct {
		Tasks []struct {
			PassRateCI *ConfidenceIntervalResponse `json:"passRateCI"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(expected, &oracle))
	require.Len(t, oracle.Tasks, 1)
	require.NotNil(t, oracle.Tasks[0].PassRateCI)
	require.NotNil(t, detail.Tasks[0].PassRateCI)
	want, actual := oracle.Tasks[0].PassRateCI, *detail.Tasks[0].PassRateCI
	// Allow two ULPs of platform-dependent Wilson endpoint variation only.
	require.InDelta(t, want.Lower, actual.Lower, 2*math.Abs(math.Nextafter(want.Lower, math.Inf(1))-want.Lower))
	require.InDelta(t, want.Upper, actual.Upper, 2*math.Abs(math.Nextafter(want.Upper, math.Inf(1))-want.Upper))
	actual.Lower, actual.Upper = want.Lower, want.Upper
	detail.Tasks[0].PassRateCI = &actual
	normalized, err := json.Marshal(detail)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(normalized))
}

func TestPromptJudgingAPIWithholdsTamperedCrossRunAndUnboundClaims(t *testing.T) {
	for _, problem := range []string{"bundle", "origin", "delivery", "selection", "state", "inspection", "cached"} {
		t.Run(problem, func(t *testing.T) {
			outcome := promptJudgingProducer(t)
			run := &outcome.TestOutcomes[0].Runs[0]
			switch problem {
			case "bundle":
				run.JudgeEvidence.SHA256 = "bad"
			case "origin":
				outcome.RunID = "other"
			case "delivery":
				run.PromptJudging[0].DeliverySHA256 = run.PromptJudging[1].DeliverySHA256
			case "selection":
				run.PromptJudging[0].EvidenceContext.Files = []string{"b.txt"}
			case "state":
				run.PromptJudging[0].SubmissionObserved = false
			case "inspection":
				run.PromptJudging[0].Inspection = "inspected"
			case "cached":
				outcome.TestOutcomes[0].Cached = true
			}
			before, err := json.Marshal(outcome)
			require.NoError(t, err)
			row := outcomeToDetail(outcome).Tasks[0].PromptJudgingRuns[0]
			if problem == "bundle" || problem == "origin" || problem == "cached" {
				require.Equal(t, "invalid", row.Assessment)
				require.Empty(t, row.Observations)
			} else {
				require.Equal(t, "invalid", row.Observations[0].State)
				require.Empty(t, row.Observations[0].DeliverySHA256)
				require.Nil(t, row.Observations[0].Usage)
			}
			after, err := json.Marshal(outcome)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestPromptJudgingSnapshotReferencesFrozenBundleWithoutCopyingItsBodies(t *testing.T) {
	outcome := promptJudgingProducer(t)
	run := outcome.TestOutcomes[0].Runs[0]
	snap, err := snapshot.Capture(snapshot.CaptureInput{EvalID: outcome.RunID,
		Task: &models.TestCase{TestID: "report-task"}, Run: &run})
	require.NoError(t, err)
	require.Equal(t, "1.2", snap.SchemaVersion)
	require.Equal(t, run.JudgeEvidence.SHA256, snap.JudgeEvidenceSHA256)
	encoded, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_FILE_BODY")
}
