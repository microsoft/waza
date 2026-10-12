package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestZeroFileSelectedCaptureUsesPrivatePublication(t *testing.T) {
	for _, paths := range [][]string{{"missing.txt"}, {"identity@example.com"}, {"../outside"}} {
		t.Run(paths[0], func(t *testing.T) {
			snap, err := Capture(CaptureInput{EvalID: "eval", Task: &models.TestCase{TestID: "task"},
				Run: &models.RunResult{RunNumber: 1, Attempts: 1, WorkspaceDir: t.TempDir()}, WorkspacePaths: paths})
			require.NoError(t, err)
			require.Empty(t, snap.WorkspaceFiles)
			require.True(t, snap.workspaceCaptureRequested)
			require.NoError(t, evidence.Validate(snap.Evidence))
			root := filepath.Join(t.TempDir(), "private")
			writer := NewWriter(root)
			first, err := writer.Write(snap)
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				require.ErrorContains(t, err, "unsupported")
				require.Empty(t, first)
				_, statErr := os.Stat(root)
				require.ErrorIs(t, statErr, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			second, err := writer.Write(snap)
			require.NoError(t, err)
			require.NotEqual(t, first, second)
			require.NotEqual(t, filepath.Join(root, "task-run1.json"), first)
			for path, mode := range map[string]os.FileMode{root: 0o700, first: 0o600, second: 0o600} {
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, mode, info.Mode().Perm())
			}
			loaded, err := LoadSnapshotFile(first)
			require.NoError(t, err)
			require.Empty(t, loaded.WorkspaceFiles)
			require.NoError(t, evidence.Validate(loaded.Evidence))
			require.Equal(t, snap.Evidence, loaded.Evidence)
		})
	}
}

func TestZeroFileSelectedCaptureRejectsLegacyDestination(t *testing.T) {
	snap, err := Capture(CaptureInput{Task: &models.TestCase{TestID: "task"},
		Run: &models.RunResult{RunNumber: 1, WorkspaceDir: t.TempDir()}, WorkspacePaths: []string{"missing.txt"}})
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o755))
	path, err := NewWriter(root).Write(snap)
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		require.ErrorContains(t, err, "0700")
	} else {
		require.ErrorContains(t, err, "unsupported")
	}
	require.Empty(t, path)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSelectedCaptureIntentIsImmediateAndNotSerialized(t *testing.T) {
	for _, requested := range []bool{false, true} {
		in := CaptureInput{Task: &models.TestCase{TestID: "task"},
			Run: &models.RunResult{RunNumber: 1, WorkspaceDir: t.TempDir()}}
		if requested {
			in.WorkspacePaths = []string{"missing.txt"}
		}
		snap, err := Capture(in)
		require.NoError(t, err)
		require.Equal(t, requested, snap.workspaceCaptureRequested)
		data, err := json.Marshal(snap)
		require.NoError(t, err)
		require.NotContains(t, string(data), "workspaceCaptureRequested")
		loaded, err := ParseSnapshot(data, "memory")
		require.NoError(t, err)
		require.False(t, loaded.workspaceCaptureRequested)
		require.Empty(t, loaded.WorkspaceFiles)
		root := filepath.Join(t.TempDir(), "legacy")
		writer := NewWriter(root)
		first, err := writer.Write(loaded)
		require.NoError(t, err)
		second, err := writer.Write(loaded)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(root, "task-run1.json"), first)
		require.Equal(t, first, second)
		if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			info, err := os.Stat(first)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
			info, err = os.Stat(root)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
		}
	}
	manual := &Snapshot{Task: SnapshotTask{TestID: "task", RunNumber: 1}}
	path, err := NewWriter(t.TempDir()).Write(manual)
	require.NoError(t, err)
	require.Equal(t, "task-run1.json", filepath.Base(path))
}
