package graders

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// gradeWithContextHooks runs the helper program with per-grader hooks and
// returns the grading error and the context file path Waza created.
func gradeWithContextHooks(t *testing.T, mode string, hooks programContextHooks) (string, error) {
	t.Helper()
	g := newProgramContextHelperGrader(t, mode)
	var path string
	created := hooks.created
	hooks.created = func(p string) {
		path = p
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(filepath.Dir(p))) })
		if created != nil {
			created(p)
		}
	}
	g.contextHooks = hooks
	_, err := g.Grade(t.Context(), &Context{Output: "out", WorkspaceDir: t.TempDir()})
	require.NotEmpty(t, path, "context file must have been created")
	return path, err
}

func halfWriteFailure(write func([]byte) (int, error), data []byte) (int, error) {
	n, _ := write(data[:len(data)/2])
	return n, errors.New("injected disk full")
}

func TestProgramContextFile_IsPrivateWhileProgramRuns(t *testing.T) {
	var contextPath string
	checked := false
	path, err := gradeWithContextHooks(t, "pass", programContextHooks{
		created: func(p string) { contextPath = p },
		remove: func(remove func() error) error {
			assertPrivateContextFile(t, contextPath)
			checked = true
			return remove()
		},
	})
	require.NoError(t, err)
	require.True(t, checked)
	require.NoDirExists(t, filepath.Dir(path))
}

func TestProgramContextFile_RemovalFailureAfterPassingProgramIsGradingError(t *testing.T) {
	path, err := gradeWithContextHooks(t, "pass", programContextHooks{
		remove: func(func() error) error { return errors.New("injected unlink failure") },
	})
	require.ErrorContains(t, err, "injected unlink failure")
	require.ErrorContains(t, err, "removing grading context directory "+filepath.Dir(path))
	require.ErrorContains(t, err, "transcript data may remain")
	require.FileExists(t, path, "residue must remain locatable at the reported path")
}

func TestProgramContextFile_CloseFailureCleansUpBeforeRunning(t *testing.T) {
	path, err := gradeWithContextHooks(t, "pass", programContextHooks{
		close: func(close func() error) error {
			return errors.Join(close(), errors.New("injected close failure"))
		},
	})
	require.ErrorContains(t, err, "injected close failure")
	require.ErrorContains(t, err, path)
	require.NoDirExists(t, filepath.Dir(path))
}

func TestProgramContextFile_PartialWriteFailureCleansUpBeforeRunning(t *testing.T) {
	path, err := gradeWithContextHooks(t, "litter", programContextHooks{write: halfWriteFailure})
	require.ErrorContains(t, err, "writing grading context")
	require.ErrorContains(t, err, "injected disk full")
	require.NoDirExists(t, filepath.Dir(path), "partial context and its directory must be removed; the program must not run")
}

func TestProgramContextFile_ShortWriteIsDetected(t *testing.T) {
	path, err := gradeWithContextHooks(t, "pass", programContextHooks{
		write: func(write func([]byte) (int, error), data []byte) (int, error) {
			return write(data[:1])
		},
	})
	require.ErrorContains(t, err, "short write")
	require.NoDirExists(t, filepath.Dir(path))
}

func TestProgramContextFile_PartialWriteWithRemovalFailureReportsResidue(t *testing.T) {
	path, err := gradeWithContextHooks(t, "pass", programContextHooks{
		write:  halfWriteFailure,
		remove: func(func() error) error { return errors.New("injected unlink failure") },
	})
	require.ErrorContains(t, err, "injected disk full")
	require.ErrorContains(t, err, "injected unlink failure")
	require.ErrorContains(t, err, filepath.Dir(path))
	require.FileExists(t, path)
}

func TestProgramContextFile_ProgramOutputInContextDirectoryIsRemoved(t *testing.T) {
	path, err := gradeWithContextHooks(t, "litter", programContextHooks{})
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Dir(path))
}

func TestProgramContextFile_RenamedAndReplacedDirectoryIsNotRemoved(t *testing.T) {
	path, err := gradeWithContextHooks(t, "swapdir", programContextHooks{})
	dir := filepath.Dir(path)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir+".moved")) })
	require.ErrorContains(t, err, "grading context directory "+dir+" was replaced")
	require.ErrorContains(t, err, "transcript data may remain elsewhere")
	data, readErr := os.ReadFile(filepath.Join(dir, "user.txt"))
	require.NoError(t, readErr, "the replacement directory and its contents must be preserved")
	require.Equal(t, "user data", string(data))
	require.FileExists(t, filepath.Join(dir+".moved", programContextFileName), "the moved original is reported, not silently left")
}

func TestProgramContextFile_ReplacedContextFileIsNotRemoved(t *testing.T) {
	path, err := gradeWithContextHooks(t, "swapfile", programContextHooks{})
	require.ErrorContains(t, err, "grading context file "+path+" was replaced")
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr, "the replacement file must be preserved")
	require.Equal(t, "user data", string(data))
}

func TestProgramContextFile_MovedContextFileIsReported(t *testing.T) {
	moved := filepath.Join(t.TempDir(), "moved.json")
	t.Setenv(programContextMoveEnv, moved)
	path, err := gradeWithContextHooks(t, "movefile", programContextHooks{})
	require.ErrorContains(t, err, "grading context file "+path+" was moved or deleted")
	require.FileExists(t, moved)
	require.NoDirExists(t, filepath.Dir(path), "Waza's own directory is still removed")
}
