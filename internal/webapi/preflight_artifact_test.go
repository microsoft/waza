package webapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestFileStoreExcludesPreflightAndKeepsLegacyResults(t *testing.T) {
	dir := t.TempDir()
	legacy, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.json"), legacy, 0600))
	for _, data := range []string{
		`{"kind":"waza.preflight","schemaVersion":"1.0","tasks":[]}`,
		`{"kind":"waza\u002epreflight","schemaVersion":"1.0","tasks":[]}`,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "preflight.json"), []byte(data), 0600))
		store := NewFileStore(dir)
		runs, err := store.ListRuns("", "")
		require.NoError(t, err)
		require.Len(t, runs, 1)
		require.Equal(t, "compatibility-run", runs[0].ID)
	}
}
