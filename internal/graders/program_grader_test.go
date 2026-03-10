package graders

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestProgramGrader_Basic(t *testing.T) {
	g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "echo",
		Args: []string{"hello"},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindProgram, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestProgramGrader_Constructor(t *testing.T) {
	t.Run("requires command", func(t *testing.T) {
		_, err := NewProgramGrader("test", models.ProgramGraderParameters{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "must have a 'command'")
	})

	t.Run("default timeout is 30 seconds", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "echo"})
		require.NoError(t, err)
		require.Equal(t, defaultProgramTimeoutSeconds, int(g.timeout.Seconds()))
	})

	t.Run("custom timeout is respected", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "echo",
			Timeout: 60,
		})
		require.NoError(t, err)
		require.Equal(t, 60, int(g.timeout.Seconds()))
	})
}

func TestProgramGrader_Grade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping program grader tests on Windows")
	}

	t.Run("program exits 0 passes with score 1.0", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "sh",
			Args: []string{"-c", "echo grading passed"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test output",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "grading passed", results.Feedback)
	})

	t.Run("program exits non-zero fails with score 0.0", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "sh",
			Args: []string{"-c", "echo fail reason; exit 1"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test output",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "Program exited with error")
	})

	t.Run("agent output is passed via stdin", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "sh",
			Args: []string{"-c", "cat"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "hello from stdin",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "hello from stdin", results.Feedback)
	})

	t.Run("workspace dir is available as env var", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "sh",
			Args: []string{"-c", "echo $WAZA_WORKSPACE_DIR"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test",
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, tmpDir, results.Feedback)
	})

	t.Run("program not found returns failure result", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "/nonexistent/program"})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("script file as command", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, "grade.sh")
		require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho \"script ran\"\nexit 0\n"), 0o755))

		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: scriptPath})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test output",
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "script ran", results.Feedback)
	})

	t.Run("result details contains expected fields", func(t *testing.T) {
		g, err := NewProgramGrader("detail-test", models.ProgramGraderParameters{Command: "echo",
			Args: []string{"ok"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindProgram, results.Type)
		require.Equal(t, "echo", results.Details["command"])
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewProgramGrader("test", models.ProgramGraderParameters{Command: "echo",
			Args: []string{"ok"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

func TestProgramGrader_ViaCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping program grader tests on Windows")
	}

	t.Run("Create with GraderKindProgram works", func(t *testing.T) {
		g, err := Create("from-create", models.ProgramGraderParameters{
			Command: "echo",
			Args:    []string{"graded"},
			Timeout: 10,
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindProgram, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Output:       "test",
			WorkspaceDir: t.TempDir(),
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure programGrader satisfies the Grader interface at compile time.
var _ Grader = (*programGrader)(nil)
