package storage

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestExplicitLocalStoragePreservesEvidenceAndOldArtifacts(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store := NewLocalStore(dir)
	snap, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "evidence-run", Task: &models.TestCase{TestID: "task"}, ExecutionMode: "mock",
		Run: &models.RunResult{RunNumber: 1, Attempts: 1},
	})
	require.NoError(t, err)
	current := makeOutcome("evidence-run", "skill", "mock", 1, 1)
	current.TestOutcomes = []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{{
		RunNumber: 1, Attempts: 1, Evidence: snap.Evidence,
		RequirementExplanations: []models.RequirementExplanation{{TaskID: "task", RequirementID: "unassessed"}},
	}}}}
	require.NoError(t, store.Upload(ctx, current))
	require.NoError(t, store.Upload(ctx, makeOutcome("legacy-run", "skill", "mock", 1, 1)))
	fresh := NewLocalStore(dir)
	loaded, err := fresh.Download(ctx, "evidence-run")
	require.NoError(t, err)
	manifest := loaded.TestOutcomes[0].Runs[0].Evidence
	require.NoError(t, evidence.Validate(manifest))
	require.Equal(t, snap.Evidence.SHA256, manifest.SHA256)
	require.Equal(t, "unassessed", loaded.TestOutcomes[0].Runs[0].RequirementExplanations[0].RequirementID)
	legacy, err := fresh.Download(ctx, "legacy-run")
	require.NoError(t, err)
	require.Empty(t, legacy.TestOutcomes)
	listed, err := fresh.List(ctx, ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 2)
}
