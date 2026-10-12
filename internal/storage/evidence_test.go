package storage

import (
	"context"
	"os"
	"path/filepath"
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
	sourcePath := filepath.Join(t.TempDir(), "source-snapshot.json")
	sourceBytes := []byte(`{"workspaceFiles":[{"path":"output.txt","content":"snapshot-only-content"}]}`)
	require.NoError(t, os.WriteFile(sourcePath, sourceBytes, 0o600))
	current := makeOutcome("evidence-run", "skill", "mock", 1, 1)
	current.TestOutcomes = []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{{
		RunNumber: 1, Attempts: 1, Evidence: snap.Evidence, SnapshotPath: sourcePath,
		RequirementExplanations: []models.RequirementExplanation{{TaskID: "task", RequirementID: "unassessed"}},
	}}}}
	require.NoError(t, store.Upload(ctx, current))
	resultBytes, err := os.ReadFile(filepath.Join(dir, "evidence-run.json"))
	require.NoError(t, err)
	require.NotContains(t, string(resultBytes), "snapshot-only-content")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "storage must not copy separately selected snapshot files")
	require.NoError(t, os.Remove(sourcePath))
	require.NoError(t, store.Upload(ctx, current), "a stored snapshot path is metadata, not a source to fetch")
	require.NoError(t, store.Upload(ctx, makeOutcome("legacy-run", "skill", "mock", 1, 1)))
	fresh := NewLocalStore(dir)
	loaded, err := fresh.Download(ctx, "evidence-run")
	require.NoError(t, err)
	manifest := loaded.TestOutcomes[0].Runs[0].Evidence
	require.NoError(t, evidence.Validate(manifest))
	require.Equal(t, snap.Evidence.SHA256, manifest.SHA256)
	require.Equal(t, sourcePath, loaded.TestOutcomes[0].Runs[0].SnapshotPath)
	require.Equal(t, "unassessed", loaded.TestOutcomes[0].Runs[0].RequirementExplanations[0].RequirementID)
	legacy, err := fresh.Download(ctx, "legacy-run")
	require.NoError(t, err)
	require.Empty(t, legacy.TestOutcomes)
	listed, err := fresh.List(ctx, ListOptions{})
	require.NoError(t, err)
	require.Len(t, listed, 2)
}
