package storage

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

func TestDashboardLocalStoreDetectsSameSizeChangeWithRestoredMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.json")
	before := makeOutcome("cached", "alpha", "mock-model", 1, 1)
	after := makeOutcome("cached", "bravo", "mock-model", 1, 1)
	beforeData, err := json.Marshal(before)
	require.NoError(t, err)
	afterData, err := json.Marshal(after)
	require.NoError(t, err)
	require.Len(t, afterData, len(beforeData))

	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(path, beforeData, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	store := NewDashboardLocalStore(dir)
	loadedBefore, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, "alpha", loadedBefore.SkillTested)
	require.NoError(t, store.Reload())
	unchanged, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Same(t, loadedBefore, unchanged, "unchanged content should reuse the parsed outcome")

	require.NoError(t, os.WriteFile(path, afterData, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	require.NoError(t, store.Reload())
	loadedAfter, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, "bravo", loadedAfter.SkillTested)
	require.NotSame(t, loadedBefore, loadedAfter)
}

func TestDashboardLocalStoreRejectsSameSizeInvalidChangeWithRestoredMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.json")
	outcome := makeOutcome("cached", "alpha", "mock-model", 1, 1)
	data, err := json.Marshal(outcome)
	require.NoError(t, err)
	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	store := NewDashboardLocalStore(dir)
	before, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)

	invalid := bytes.Repeat([]byte{' '}, len(data))
	invalid[0] = '{'
	require.NoError(t, os.WriteFile(path, invalid, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	require.ErrorContains(t, store.Reload(), path)

	after, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Same(t, before, after, "failed refresh should preserve the prior snapshot")
}

func BenchmarkLocalResultRefresh300(b *testing.B) {
	dir := b.TempDir()
	for i := range 300 {
		outcome := makeOutcome("run-"+strconv.Itoa(i), "skill", "mock-model", 5, 10)
		data, err := json.Marshal(outcome)
		require.NoError(b, err)
		require.NoError(b, os.WriteFile(filepath.Join(dir, outcome.RunID+".json"), data, 0o600))
	}

	b.Run("uncached", func(b *testing.B) {
		_, files, err := ReadLocalResults(dir, nil)
		require.NoError(b, err)
		b.ResetTimer()
		for range b.N {
			_, files, err = ReadLocalResults(dir, files)
			require.NoError(b, err)
		}
	})
	b.Run("dashboard-cache", func(b *testing.B) {
		cache := NewDashboardResultCache()
		_, files, err := ReadLocalResultsForDashboard(dir, nil, cache)
		require.NoError(b, err)
		b.ResetTimer()
		for range b.N {
			_, files, err = ReadLocalResultsForDashboard(dir, files, cache)
			require.NoError(b, err)
		}
	})
}
