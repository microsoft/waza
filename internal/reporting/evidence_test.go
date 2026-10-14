package reporting

import (
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSummaryWithholdsInvalidEvidenceAndItsRequirementObservations(t *testing.T) {
	manifest := &models.EvidenceManifest{
		Origin:  models.EvidenceOrigin{EvalID: "source", TaskID: "task", RunNumber: 1, AttemptCount: 1},
		Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
	}
	require.NoError(t, evidence.Seal(manifest))
	outcome := &models.EvaluationOutcome{RunID: "current", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Runs: []models.RunResult{{RunNumber: 1, Attempts: 1, Evidence: manifest,
			RequirementExplanations: []models.RequirementExplanation{{RequirementID: "do-not-display",
				Checks: []models.CheckExplanation{{Observation: "grader_recorded_pass"}}}},
		}},
	}}}
	report := FormatSummaryReport(outcome)
	require.Contains(t, report, "Run 1 evidence: invalid metadata")
	require.NotContains(t, report, "do-not-display")
	require.NotContains(t, report, "grader_recorded_pass")
	outcome.TestOutcomes[0].Cached = true
	report = FormatSummaryReport(outcome)
	require.Contains(t, report, "source eval source")
	require.Contains(t, report, "do-not-display")
	require.Equal(t, "source", manifest.Origin.EvalID)
}

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

func TestSummaryWithholdsForeignRequirementReferences(t *testing.T) {
	manifest := &models.EvidenceManifest{
		Origin:    models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
		Runtime:   models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		Artifacts: []models.EvidenceArtifact{{ID: "receipt", Availability: "not_requested", Completeness: "unknown", Reason: "Not captured."}},
	}
	require.NoError(t, evidence.Seal(manifest))
	reference, err := evidence.Reference(manifest, "receipt")
	require.NoError(t, err)
	outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Runs: []models.RunResult{{RunNumber: 1, Attempts: 1, Evidence: manifest,
			RequirementExplanations: []models.RequirementExplanation{{TaskID: "task", RequirementID: "do-not-display",
				Checks: []models.CheckExplanation{{References: []models.EvidenceReference{reference}}}}},
		}},
	}}}
	report := FormatSummaryReport(outcome)
	require.Contains(t, report, "do-not-display")
	require.NotContains(t, report, "invalid references")
	outcome.TestOutcomes[0].Runs[0].RequirementExplanations[0].Checks[0].References[0].Origin.TaskID = "foreign"
	report = FormatSummaryReport(outcome)
	require.Contains(t, report, "invalid references")
	require.NotContains(t, report, "do-not-display")
	outcome.TestOutcomes[0].Runs[0].Evidence = nil
	report = FormatSummaryReport(outcome)
	require.Contains(t, report, "invalid references")
	require.NotContains(t, report, "do-not-display")
}
