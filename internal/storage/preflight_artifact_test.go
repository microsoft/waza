package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestLocalStoreExcludesPreflightAndKeepsLegacyResults(t *testing.T) {
	dir := t.TempDir()
	legacy, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.json"), legacy, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "preflight.json"), []byte(`{"kind":"waza.preflight","schemaVersion":"1.0","tasks":[]}`), 0600))
	store := NewLocalStore(dir)
	runs, err := store.List(context.Background(), ListOptions{})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	_, err = store.Download(context.Background(), "preflight")
	require.ErrorIs(t, err, ErrNotFound)
}
