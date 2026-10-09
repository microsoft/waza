package reporting

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSummaryExplainsEveryAssessedTrialWithoutChangingLegacyOutput(t *testing.T) {
	outcome := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{{RunNumber: 1}}}}}
	legacy := FormatSummaryReport(outcome)
	require.NotContains(t, legacy, "requirement")
	outcome.TestOutcomes[0].Runs = []models.RunResult{
		{RunNumber: 1, RequirementExplanations: []models.RequirementExplanation{{RequirementID: "boundary",
			Checks: []models.CheckExplanation{{Observation: "grader_recorded_failure", Category: "operational_status_unavailable"}}}}},
		{RunNumber: 2, RequirementExplanations: []models.RequirementExplanation{{RequirementID: "boundary",
			Checks: []models.CheckExplanation{{Observation: "unresolved", Category: "insufficient_evidence"}}}}},
	}
	report := FormatSummaryReport(outcome)
	require.Contains(t, report, "Run 1 requirement boundary")
	require.Contains(t, report, "operational_status_unavailable")
	require.Contains(t, report, "Run 2 requirement boundary")
	require.Contains(t, report, "insufficient_evidence")
}
