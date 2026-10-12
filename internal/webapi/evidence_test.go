package webapi

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEvidenceAPILegacyJSONOmissionAndInvalidObservationWithholding(t *testing.T) {
	outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Runs: []models.RunResult{{RunNumber: 1, Attempts: 1}},
	}}}
	data, err := json.Marshal(outcomeToDetail(outcome))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"evidenceRuns"`)
	run := &outcome.TestOutcomes[0].Runs[0]
	run.RequirementExplanations = []models.RequirementExplanation{{TaskID: "task", RequirementID: "req",
		Checks: []models.CheckExplanation{{Observation: "unresolved", Category: "insufficient_evidence"}},
	}}
	rows := outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Len(t, rows, 1)
	require.Equal(t, "unassessed", rows[0].Assessment)
	require.Equal(t, run.RequirementExplanations, rows[0].Explanations)
	for _, invalid := range []string{"wrong-origin", "tampered"} {
		t.Run(invalid, func(t *testing.T) {
			manifest := &models.EvidenceManifest{
				Origin:  models.EvidenceOrigin{EvalID: "other", TaskID: "task", RunNumber: 1, AttemptCount: 1},
				Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
			}
			require.NoError(t, evidence.Seal(manifest))
			if invalid == "tampered" {
				manifest.SHA256 = "tampered"
			}
			run.Evidence = manifest
			row := outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
			require.Equal(t, "invalid", row.Assessment)
			require.Nil(t, row.Manifest)
			require.Empty(t, row.Explanations)
			data, err := json.Marshal(row)
			require.NoError(t, err)
			require.NotContains(t, string(data), `"manifest"`)
			require.NotContains(t, string(data), `"explanations"`)
			require.Equal(t, "req", run.RequirementExplanations[0].RequirementID)
		})
	}
}

func TestEvidenceAPIIncludesEveryRunAndHistoricalAbsence(t *testing.T) {
	manifest := &models.EvidenceManifest{
		Origin:    models.EvidenceOrigin{EvalID: "source", TaskID: "task", RunNumber: 2, AttemptCount: 3},
		Runtime:   models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		Artifacts: []models.EvidenceArtifact{{ID: "workspace", Availability: "not_requested", Completeness: "unknown", Reason: "No workspace requested."}},
	}
	require.NoError(t, evidence.Seal(manifest))
	outcome := &models.EvaluationOutcome{RunID: "current", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Cached: true, Runs: []models.RunResult{{RunNumber: 1}, {RunNumber: 2, Attempts: 3, Evidence: manifest}},
	}}}
	rows := outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Len(t, rows, 2)
	require.Equal(t, "unassessed", rows[0].Assessment)
	require.Nil(t, rows[0].Manifest)
	require.Equal(t, "recorded", rows[1].Assessment)
	require.True(t, rows[1].Cached)
	require.Equal(t, "source", rows[1].Manifest.Origin.EvalID)
	manifest.SHA256 = "tampered"
	rows = outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Equal(t, "invalid", rows[1].Assessment)
	require.Nil(t, rows[1].Manifest)
}
