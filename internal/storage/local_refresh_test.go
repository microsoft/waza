package storage

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalStoreMissingEmptyDirectoryRemainsEmpty(t *testing.T) {
	store := NewLocalStore(filepath.Join(t.TempDir(), "not-created"))
	for range 3 {
		list, err := store.List(t.Context(), ListOptions{})
		require.NoError(t, err)
		require.Empty(t, list)
		require.NoError(t, store.Reload())
	}
}

func TestReadLocalResultsWalkErrorsRespectAdmission(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "known", "cached.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	data, err := json.Marshal(makeOutcome("cached", "skill", "mock", 1, 1))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0o600))
	info, err := os.Stat(file)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path string
		fail       bool
	}{
		{"root", dir, true},
		{"known-file", file, true},
		{"known-subtree", filepath.Dir(file), true},
		{"new-file", filepath.Join(dir, "new.json"), false},
		{"unrelated-subtree", filepath.Join(dir, "unrelated"), false},
		{"similar-prefix-not-known", filepath.Join(dir, "known-other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk := func(_ string, visit fs.WalkDirFunc) error {
				if err := visit(file, fs.FileInfoToDirEntry(info), nil); err != nil {
					return err
				}
				return visit(tc.path, nil, &os.PathError{Op: "readdir", Path: tc.path, Err: fs.ErrPermission})
			}
			results, files, err := readLocalResults(dir, map[string]struct{}{file: {}}, walk)
			if tc.fail {
				require.ErrorIs(t, err, fs.ErrPermission)
				require.ErrorContains(t, err, tc.path)
				require.Nil(t, results)
				require.Nil(t, files)
			} else {
				require.NoError(t, err)
				require.Contains(t, results, "cached")
				require.Contains(t, files, file)
			}
		})
	}
}

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
