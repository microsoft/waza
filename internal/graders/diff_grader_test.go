package graders

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiffGrader_SnapshotMismatchFailsWithoutUpdate(t *testing.T) {
	workspaceDir := t.TempDir()
	contextDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "output.txt"), []byte("new content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "expected.txt"), []byte("old content"), 0o644))

	grader, err := NewDiffGrader(DiffGraderArgs{
		Name: "diff",
		ExpectedFiles: []ExpectedFile{
			{
				Path:     "output.txt",
				Snapshot: "expected.txt",
			},
		},
		ContextDir: contextDir,
	})
	require.NoError(t, err)

	result, err := grader.Grade(context.Background(), &Context{WorkspaceDir: workspaceDir})
	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Feedback, "does not match snapshot")
}

func TestDiffGrader_UpdateSnapshots_UpdatesAndCreatesSnapshots(t *testing.T) {
	workspaceDir := t.TempDir()
	contextDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "changed.txt"), []byte("line 1\nline 2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "new.txt"), []byte("brand new\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(contextDir, "expected"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "expected", "changed.txt"), []byte("old line\n"), 0o644))

	grader, err := NewDiffGrader(DiffGraderArgs{
		Name: "diff",
		ExpectedFiles: []ExpectedFile{
			{
				Path:     "changed.txt",
				Snapshot: filepath.Join("expected", "changed.txt"),
			},
			{
				Path:     "new.txt",
				Snapshot: filepath.Join("expected", "new.txt"),
			},
		},
		ContextDir:      contextDir,
		UpdateSnapshots: true,
	})
	require.NoError(t, err)

	result, err := grader.Grade(context.Background(), &Context{WorkspaceDir: workspaceDir})
	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Contains(t, result.Feedback, "snapshots:")
	assert.Contains(t, result.Feedback, "1 updated")
	assert.Contains(t, result.Feedback, "1 created")

	updatedSnapshot, err := os.ReadFile(filepath.Join(contextDir, "expected", "changed.txt"))
	require.NoError(t, err)
	assert.Equal(t, "line 1\nline 2\n", string(updatedSnapshot))

	createdSnapshot, err := os.ReadFile(filepath.Join(contextDir, "expected", "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "brand new\n", string(createdSnapshot))
}

func TestDiffGrader_UpdateSnapshots_NoChangesFlow(t *testing.T) {
	workspaceDir := t.TempDir()
	contextDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "stable.txt"), []byte("same\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(contextDir, "expected.txt"), []byte("same\n"), 0o644))

	grader, err := NewDiffGrader(DiffGraderArgs{
		Name: "diff",
		ExpectedFiles: []ExpectedFile{
			{
				Path:     "stable.txt",
				Snapshot: "expected.txt",
			},
		},
		ContextDir:      contextDir,
		UpdateSnapshots: true,
	})
	require.NoError(t, err)

	result, err := grader.Grade(context.Background(), &Context{WorkspaceDir: workspaceDir})
	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Contains(t, result.Feedback, "0 updated")
	assert.Contains(t, result.Feedback, "0 created")
	assert.Contains(t, result.Feedback, "1 unchanged")
}

func TestDiffGrader_UpdateSnapshots_BlocksPathTraversal(t *testing.T) {
	workspaceDir := t.TempDir()
	contextDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideSnapshot := filepath.Join(outsideDir, "escape.txt")

	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, "stable.txt"), []byte("same\n"), 0o644))

	absContextDir, err := filepath.Abs(contextDir)
	require.NoError(t, err)
	absOutsideSnapshot, err := filepath.Abs(outsideSnapshot)
	require.NoError(t, err)
	if filepath.VolumeName(absContextDir) != filepath.VolumeName(absOutsideSnapshot) {
		t.Skipf(
			"skipping traversal test: contextDir (%s) and outsideSnapshot (%s) are on different volumes",
			absContextDir,
			absOutsideSnapshot,
		)
	}

	relEscape, err := filepath.Rel(absContextDir, absOutsideSnapshot)
	require.NoError(t, err)
	require.NotContains(t, relEscape, ":", "expected a relative path for traversal test")

	grader, err := NewDiffGrader(DiffGraderArgs{
		Name: "diff",
		ExpectedFiles: []ExpectedFile{
			{
				Path:     "stable.txt",
				Snapshot: relEscape,
			},
		},
		ContextDir:      contextDir,
		UpdateSnapshots: true,
	})
	require.NoError(t, err)

	result, err := grader.Grade(context.Background(), &Context{WorkspaceDir: workspaceDir})
	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Feedback, "Invalid snapshot file")

	_, statErr := os.Stat(outsideSnapshot)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
