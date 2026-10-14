package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadLocalResultReadFailuresRespectAdmission(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, name := range []string{"missing-file", "directory"} {
			t.Run(name+"/"+map[bool]string{false: "new", true: "known"}[known], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "result.json")
				if name == "directory" {
					require.NoError(t, os.Mkdir(path, 0o700))
				}
				outcome, err := readLocalResult(path, known)
				require.Nil(t, outcome)
				if known {
					require.ErrorContains(t, err, path)
				} else {
					require.NoError(t, err, "unadmitted unreadable files remain skippable")
				}
			})
		}
	}
}

func TestLocalStoreReloadDirectoryFailurePreservesCache(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStore(dir)
	outcome := makeOutcome("cached", "skill", "mock-model", 1, 1)
	require.NoError(t, store.Upload(t.Context(), outcome))
	before, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	for name, path := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "missing"),
		"file":    blocker,
		"invalid": filepath.Join(blocker, "results"),
	} {
		t.Run(name, func(t *testing.T) {
			store.dir = path
			require.Error(t, store.Reload())
			after, err := store.Download(t.Context(), "cached")
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
	store.dir = dir
	require.NoError(t, store.Reload())
	after, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
