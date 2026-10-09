package webapi

import (
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

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
