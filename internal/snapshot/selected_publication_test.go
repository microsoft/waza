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
	for _, tt := range []struct {
		name, path, diagnostic string
		zeroLimits             bool
	}{
		{"missing-file", "missing.txt", "approved file is unavailable or has an unsupported symlink or ancestor", false},
		{"redacted-identity", "identity@example.com", "file identity matches a redaction rule and cannot be captured", false},
		{"outside-path", "../outside", "invalid slash-relative path", false},
		{"zero-limits", "missing.txt", "positive file and total byte limits are required", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limits := WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 2048}
			if tt.zeroLimits {
				limits = WorkspaceLimits{}
			}
			snap, err := Capture(CaptureInput{EvalID: "eval", Task: &models.TestCase{TestID: "task"},
				Run:            &models.RunResult{RunNumber: 1, Attempts: 1, WorkspaceDir: t.TempDir()},
				WorkspacePaths: []string{tt.path}, WorkspaceLimits: limits})
			require.NoError(t, err)
			require.Empty(t, snap.WorkspaceFiles)
			require.True(t, snap.workspaceCaptureRequested)
			require.NoError(t, evidence.Validate(snap.Evidence))
			diagnostic := tt.diagnostic
			if !tt.zeroLimits && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				diagnostic = "safe no-symlink capture is unsupported on this platform"
			}
			require.Len(t, snap.Evidence.Diagnostics, 1)
			require.Equal(t, "capture", snap.Evidence.Diagnostics[0].Category)
			require.Equal(t, "workspace_partial", snap.Evidence.Diagnostics[0].Code)
			require.Contains(t, snap.Evidence.Diagnostics[0].Message, diagnostic)
			artifacts := make(map[string]models.EvidenceArtifact)
			for _, artifact := range snap.Evidence.Artifacts {
				artifacts[artifact.ID] = artifact
			}
			require.Contains(t, artifacts, "workspace")
			require.Equal(t, "unavailable", artifacts["workspace"].Availability)
			require.Equal(t, "partial", artifacts["workspace"].Completeness)
			require.Contains(t, artifacts, "workspace-request/1")
			require.Equal(t, "unavailable", artifacts["workspace-request/1"].Availability)
			require.Equal(t, "unknown", artifacts["workspace-request/1"].Completeness)
			require.Empty(t, artifacts["workspace-request/1"].Document)
			require.Empty(t, artifacts["workspace-request/1"].Pointer)
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
		Run: &models.RunResult{RunNumber: 1, WorkspaceDir: t.TempDir()}, WorkspacePaths: []string{"missing.txt"},
		WorkspaceLimits: WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 2048}})
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
			Run:             &models.RunResult{RunNumber: 1, WorkspaceDir: t.TempDir()},
			WorkspaceLimits: WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 2048}}
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
