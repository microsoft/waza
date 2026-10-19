package reporting

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSummaryReportsOnlyBoundPromptJudgingMetadata(t *testing.T) {
	origin := models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"}
	bundle := judgeevidence.Capture(judgeevidence.CaptureInput{Origin: origin, Task: "PRIVATE_TASK_CANARY", OutputPresent: true,
		ExecutionMode: "mock", Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: "PRIVATE_INPUT_CANARY"}},
			{Data: &copilot.AssistantMessageData{Content: "PRIVATE_OUTPUT_CANARY"}},
		}})
	require.NoError(t, judgeevidence.Verify(bundle, origin))
	zero := 0.0
	run := models.RunResult{RunNumber: 1, Attempts: 1, JudgeEvidence: bundle, PromptJudging: []models.PromptJudgeObservation{{
		Grader: "judge", State: "submitted", Purpose: "run", Context: "frozen_inline", Inspection: "not_observable",
		SubmissionObserved: true, BundleSHA256: bundle.SHA256, DeliverySHA256: bundle.SHA256,
		Usage: &models.UsageStats{AICredits: &zero},
	}}}
	outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{run}}}}
	text := FormatSummaryReport(outcome)
	require.Contains(t, text, "Run 1 attempt 1 prompt judging: recorded")
	require.Contains(t, text, "submission observed: true; judge AI credits: 0")
	require.NotContains(t, text, "PRIVATE_")
	outcome.TestOutcomes[0].Runs[0].Attempts++
	text = FormatSummaryReport(outcome)
	require.Contains(t, text, "prompt judging: invalid")
	require.NotContains(t, text, "submission observed: true")
	require.NotContains(t, text, bundle.SHA256)
}
