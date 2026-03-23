package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloneGitResource_WorktreeType(t *testing.T) {
	repoDir, commitSHA := mustCreateRepo(t)
	workspaceDir := t.TempDir()
	destName := "wt-test"

	gitRes := &models.GitResource{
		Commit:       commitSHA,
		Type:         models.GitTypeWorktree,
		Source:       repoDir,
		RelativeDest: destName,
	}

	ctx := context.Background()
	res, err := CloneGitResource(ctx, *gitRes, workspaceDir)
	require.NoError(t, err)
	require.NotNil(t, res)

	// Verify the worktree file exists
	targetDir := filepath.Join(workspaceDir, destName)
	content, err := os.ReadFile(filepath.Join(targetDir, "hello.txt"))
	require.NoError(t, err, "reading file in worktree")
	assert.Equal(t, "hello world", string(content))

	// Cleanup
	err = res.Cleanup(context.Background())
	require.NoError(t, err)

	// Verify worktree dir is removed
	_, err = os.Stat(targetDir)
	assert.True(t, os.IsNotExist(err), "worktree directory should have been removed")
}

func TestCloneGitResource_WorktreeDetachHEAD(t *testing.T) {
	repoDir, _ := mustCreateRepo(t)
	workspaceDir := t.TempDir()
	destName := "wt-detach"

	gitRes := &models.GitResource{
		Type:         models.GitTypeWorktree,
		Source:       repoDir,
		RelativeDest: destName,
	}

	ctx := context.Background()
	res, err := CloneGitResource(ctx, *gitRes, workspaceDir)
	require.NoError(t, err, "CloneGitResource (detach)")

	targetDir := filepath.Join(workspaceDir, destName)
	_, err = os.Stat(filepath.Join(targetDir, "hello.txt"))
	require.NoError(t, err, "expected hello.txt in worktree")

	// Cleanup
	err = res.Cleanup(context.Background())
	require.NoError(t, err)
}

func TestCloneGitResource_UnsupportedType(t *testing.T) {
	_, commitSHA := mustCreateRepo(t)
	workspaceDir := t.TempDir()

	gitRes := &models.GitResource{
		Commit:       commitSHA,
		Type:         "clone",
		Source:       "/tmp/repo",
		RelativeDest: "clone-test",
	}

	ctx := context.Background()
	_, err := CloneGitResource(ctx, *gitRes, workspaceDir)
	require.Error(t, err, "expected unsupported type to be rejected")
	require.Contains(t, err.Error(), "invalid repo type")
}

func TestCloneGitResource_SourceDoesNotExist(t *testing.T) {
	workspaceDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "missing-repo")

	gitRes := &models.GitResource{
		Type:         models.GitTypeWorktree,
		Source:       missingDir,
		RelativeDest: "wt-test",
	}

	_, err := CloneGitResource(context.Background(), *gitRes, workspaceDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestCloneGitResource_SourceIsNotDirectory(t *testing.T) {
	workspaceDir := t.TempDir()
	notDir := filepath.Join(t.TempDir(), "repo.txt")
	require.NoError(t, os.WriteFile(notDir, []byte("not a dir"), 0o644))

	gitRes := &models.GitResource{
		Type:         models.GitTypeWorktree,
		Source:       notDir,
		RelativeDest: "wt-test",
	}

	_, err := CloneGitResource(context.Background(), *gitRes, workspaceDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestCloneGitResource_SourceIsNotGitRepo(t *testing.T) {
	workspaceDir := t.TempDir()
	nonRepoDir := t.TempDir()

	gitRes := &models.GitResource{
		Type:         models.GitTypeWorktree,
		Source:       nonRepoDir,
		RelativeDest: "wt-test",
	}

	_, err := CloneGitResource(context.Background(), *gitRes, workspaceDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestResolveWorkDir(t *testing.T) {
	tests := []struct {
		name    string
		workDir string
		want    string
		wantErr bool
	}{
		{
			name:    "empty returns workspace root",
			workDir: "",
			want:    "/workspace",
		},
		{
			name:    "subdirectory",
			workDir: "my-repo",
			want:    "/workspace/my-repo",
		},
		{
			name:    "nested subdirectory",
			workDir: "repos/my-repo",
			want:    "/workspace/repos/my-repo",
		},
		{
			name:    "traversal rejected",
			workDir: "../../etc",
			wantErr: true,
		},
		{
			name:    "dot-dot in middle rejected",
			workDir: "a/../../outside",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveWorkDir("/workspace", tt.workDir)
			if tt.wantErr {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCreateGitResources(t *testing.T) {
	workspaceDir := t.TempDir()
	repoDir, _ := mustCreateRepo(t)

	resources := []models.GitResource{
		{Commit: "", Type: models.GitTypeWorktree, Source: repoDir, RelativeDest: "dest"},

		// will fail since we already have a worktree at 'dest'
		{Commit: "", Type: models.GitTypeWorktree, Source: repoDir, RelativeDest: "dest"},
	}

	createdResources, err := CloneGitResources(context.Background(), resources, workspaceDir)
	require.Error(t, err)
	require.Empty(t, createdResources)
	
	require.NoDirExists(t, filepath.Join(workspaceDir, "dest"))
}

// mustCreateRepo creates a repo with a single commit, with 'test.txt' in the root (contents: "hello world")
func mustCreateRepo(t *testing.T) (repoDir string, headCommitSHA string) {
	repoDir = t.TempDir()

	_, err := runGitCommand(context.Background(), repoDir, "init")
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("hello world"), 0644)
	require.NoError(t, err)

	_, err = runGitCommand(context.Background(), repoDir, "add", "hello.txt")
	require.NoError(t, err)

	_, err = runGitCommand(context.Background(), repoDir,
		"-c", "user.name=waza",
		"-c", "user.email=waza",
		"commit",
		"-m", "first and only file", "hello.txt")
	require.NoError(t, err)

	// Get commit SHA
	output, err := runGitCommand(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)

	return repoDir, strings.TrimSpace(output)
}
