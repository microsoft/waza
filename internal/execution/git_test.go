package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initSourceRepo creates a tiny local git repository with a single commit
// containing a known file. It returns the absolute path to the repo. The
// repo's user identity is set per-repo so the test doesn't depend on the
// caller's global git config (important on CI).
func initSourceRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	repoDir := t.TempDir()
	runOrFail(t, repoDir, "git", "init", "--initial-branch=main", ".")
	runOrFail(t, repoDir, "git", "config", "user.email", "waza-test@example.com")
	runOrFail(t, repoDir, "git", "config", "user.name", "Waza Test")
	// Disable autocrlf so checked-out files always use LF, matching the literal
	// "hello\n" assertions. Without this, Windows git converts to CRLF on checkout.
	runOrFail(t, repoDir, "git", "config", "core.autocrlf", "false")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644))
	runOrFail(t, repoDir, "git", "add", "README.md")
	runOrFail(t, repoDir, "git", "commit", "-m", "initial")
	return repoDir
}

func runOrFail(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "%s %s failed: %s", name, strings.Join(args, " "), string(out))
}

func TestCloneGitResources_WorktreeFromHEAD(t *testing.T) {
	source := initSourceRepo(t)
	workspace := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resources, err := CloneGitResources(ctx, []models.GitResource{{
		Type:   models.GitResourceTypeWorktree,
		Source: source,
		Dest:   "checkout",
	}}, workspace)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	t.Cleanup(func() {
		_ = resources[0].Cleanup(ctx)
	})

	// File from the source HEAD must be visible in the checkout.
	got, err := os.ReadFile(filepath.Join(workspace, "checkout", "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(got))

	// Cleanup removes the worktree.
	require.NoError(t, resources[0].Cleanup(ctx))
	_, err = os.Stat(filepath.Join(workspace, "checkout"))
	assert.True(t, os.IsNotExist(err), "expected checkout dir to be removed, got %v", err)

	// Cleanup is idempotent.
	require.NoError(t, resources[0].Cleanup(ctx))
}

func TestCloneGitResources_WorktreeWithBranchName_UsesDetach(t *testing.T) {
	source := initSourceRepo(t)
	// `main` is already checked out at the source repo. Without --detach
	// `git worktree add` would refuse with "already checked out".
	workspace := t.TempDir()
	ctx := context.Background()

	resources, err := CloneGitResources(ctx, []models.GitResource{{
		Type:   models.GitResourceTypeWorktree,
		Source: source,
		Commit: "main",
		Dest:   "wt",
	}}, workspace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resources[0].Cleanup(ctx) })

	got, err := os.ReadFile(filepath.Join(workspace, "wt", "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(got))
}

func TestCloneGitResources_RejectsMissingSource(t *testing.T) {
	workspace := t.TempDir()
	_, err := CloneGitResources(context.Background(), []models.GitResource{{
		Type: models.GitResourceTypeWorktree,
	}}, workspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source")
}

func TestCloneGitResources_RejectsTraversalDest(t *testing.T) {
	source := initSourceRepo(t)
	workspace := t.TempDir()
	_, err := CloneGitResources(context.Background(), []models.GitResource{{
		Type:   models.GitResourceTypeWorktree,
		Source: source,
		Dest:   "../outside",
	}}, workspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestCloneGitResources_RejectsUnsupportedType(t *testing.T) {
	workspace := t.TempDir()
	_, err := CloneGitResources(context.Background(), []models.GitResource{{
		Type:   "ftp",
		Source: "/tmp",
	}}, workspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestCloneGitResources_RequiresDest(t *testing.T) {
	source := initSourceRepo(t)
	workspace := t.TempDir()

	// The engine creates the workspace directory before calling
	// CloneGitResources, so `git worktree add` cannot use the workspace
	// root as its target. Dest is therefore required for the worktree
	// strategy.
	_, err := CloneGitResources(context.Background(), []models.GitResource{{
		Type:   models.GitResourceTypeWorktree,
		Source: source,
	}}, workspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dest is required")
}

func TestCloneGitResources_RejectsTraversalSegmentInDest(t *testing.T) {
	source := initSourceRepo(t)
	workspace := t.TempDir()
	// "foo/../bar" normalizes to "bar" and would slip past a post-Clean
	// check, but validation should reject the raw '..' segment outright.
	_, err := CloneGitResources(context.Background(), []models.GitResource{{
		Type:   models.GitResourceTypeWorktree,
		Source: source,
		Dest:   "foo/../bar",
	}}, workspace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestResolveWorkDir_RejectsTraversalSegment(t *testing.T) {
	ws := t.TempDir()
	_, err := ResolveWorkDir(ws, "foo/../bar")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "..")
}

func TestResolveWorkDir(t *testing.T) {
	ws := t.TempDir()

	got, err := ResolveWorkDir(ws, "")
	require.NoError(t, err)
	assert.Equal(t, ws, got)

	got, err = ResolveWorkDir(ws, "sub/dir")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(ws, "sub", "dir"), got)

	_, err = ResolveWorkDir(ws, "../escape")
	require.Error(t, err)

	_, err = ResolveWorkDir(ws, "/abs/path")
	require.Error(t, err)
}
