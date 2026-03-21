package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// GitWorkTree tracks a worktree created during task execution for cleanup.
type GitWorkTree struct {
	WorktreePath string
	RepoDir      string // the repo from which the worktree was created
}

func (gwt GitWorkTree) Cleanup(ctx context.Context) error {
	return gitWorktreeRemove(ctx, gwt)
}

// CloneGitResource checks out a git resource into the workspace directory.
// It returns a GitWorktreeInfo for cleanup.
func CloneGitResource(ctx context.Context, gitRes models.GitResource, workspaceDir string) (GitResource, error) {
	switch gitRes.Type {
	case models.GitTypeWorktree:
		targetDir := workspaceDir

		if gitRes.Dest != "" {
			targetDir = filepath.Join(workspaceDir, gitRes.Dest)
		}

		// Only worktree is supported; Validate() already enforces this.
		return gitWorkTreeAdd(ctx, gitRes.Commit, gitRes.Source, targetDir)
	default:
		return nil, fmt.Errorf("invalid repo type %q", gitRes.Type)
	}
}

type GitResource interface {
	Cleanup(ctx context.Context) error
}

// CloneGitResources materializes all git resources into the workspace.
func CloneGitResources(ctx context.Context, gitResources []models.GitResource, workspaceDir string) ([]GitResource, error) {
	var createdResources []GitResource

	for i := range gitResources {
		gr, err := CloneGitResource(ctx, gitResources[i], workspaceDir)
		if err != nil {
			return createdResources, fmt.Errorf("failed to clone git resource: %w", err)
		}
		if gr != nil {
			createdResources = append(createdResources, gr)
		}
	}

	return createdResources, nil
}

// gitWorkTreeAdd runs 'git worktree add', creating a git worktree (an incredibly cheap copy) of a local repo to
// another local path on disk. Note, this requires a local clone of a git repo to work.
func gitWorkTreeAdd(ctx context.Context, commit string, repoDir string, targetDir string) (*GitWorkTree, error) {
	args := []string{"worktree", "add"}

	if commit != "" {
		// git worktree add <path> <commit>
		args = append(args, targetDir, commit)
	} else {
		// git worktree add --detach <path>  (uses HEAD)
		args = append(args, "--detach", targetDir)
	}

	if _, err := runGitCommand(ctx, repoDir, args...); err != nil {
		return nil, fmt.Errorf("git worktree add failed: %w", err)
	}

	return &GitWorkTree{
		WorktreePath: targetDir,
		RepoDir:      repoDir,
	}, nil
}

// gitWorktreeRemove removes a worktree from a git repo, using 'git worktree remove'.
func gitWorktreeRemove(ctx context.Context, wt GitWorkTree) error {
	_, err := runGitCommand(ctx, wt.RepoDir, "worktree", "remove", "--force", wt.WorktreePath)

	if err != nil {
		return fmt.Errorf("git worktree remove %q failed: %w", wt.WorktreePath, err)
	}

	return nil
}

// runGitCommand executes a git command in the specified directory and returns stdout.
func runGitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	// From the docs: if set to false, git will not prompt on the terminal, like for credentials.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	out, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(out))

	if err != nil {
		return "", fmt.Errorf("%w: %s", err, outStr)
	}

	return outStr, nil
}
