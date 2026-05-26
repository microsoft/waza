// Package execution: git resource materialization for the per-task workspace.
//
// Git resources let task authors check out a clean copy of a local git
// repository into the workspace before the agent runs. This is useful when
// developing skills that live inside the same repo whose code they need to
// reason about (e.g. azure-sdk-for-rust): rather than hand-crafting
// fixtures, the task can point at the local clone and get an isolated
// checkout for each run.
//
// Currently only the "worktree" strategy is supported. It uses `git
// worktree add --detach` against the local source repo, which is cheap
// (shares the same .git object store) and does not require network
// access. Additional strategies (HTTPS clone, ssh, submodules) can be
// added later behind the same GitResource interface.
package execution

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// GitResource represents a materialized git resource that must be cleaned
// up when the engine shuts down. Implementations are returned from
// CloneGitResources.
type GitResource interface {
	// Cleanup removes the materialized resource (e.g. `git worktree
	// remove`). It is safe to call multiple times; subsequent calls are
	// no-ops.
	Cleanup(ctx context.Context) error
}

// CloneGitResources materializes each git resource into workspaceDir and
// returns handles that the caller is responsible for cleaning up via
// Cleanup(). If any resource fails to materialize, previously created
// resources in this call are cleaned up before returning the error.
//
// The caller must clean up resources before removing workspaceDir,
// because some strategies (worktree) keep bookkeeping inside the source
// repository's .git/worktrees/ that needs `git worktree remove` to
// invalidate cleanly.
func CloneGitResources(ctx context.Context, resources []models.GitResource, workspaceDir string) ([]GitResource, error) {
	if len(resources) == 0 {
		return nil, nil
	}

	cleanWorkspace := filepath.Clean(workspaceDir)
	if cleanWorkspace == "" {
		return nil, fmt.Errorf("workspace is not set")
	}

	created := make([]GitResource, 0, len(resources))
	for i := range resources {
		res := resources[i]
		if err := res.Validate(); err != nil {
			cleanupAll(ctx, created)
			return nil, fmt.Errorf("git resource %d: %w", i, err)
		}

		target, err := resolveDest(cleanWorkspace, res.Dest)
		if err != nil {
			cleanupAll(ctx, created)
			return nil, fmt.Errorf("git resource %d: %w", i, err)
		}

		switch res.Type {
		case models.GitResourceTypeWorktree:
			wt, err := gitWorktreeAdd(ctx, res.Source, res.Commit, target)
			if err != nil {
				cleanupAll(ctx, created)
				return nil, fmt.Errorf("git resource %d (worktree from %s): %w", i, res.Source, err)
			}
			created = append(created, wt)
		default:
			cleanupAll(ctx, created)
			return nil, fmt.Errorf("git resource %d: unsupported type %q", i, res.Type)
		}
	}

	return created, nil
}

// resolveDest joins dest under workspaceDir and verifies it stays inside
// the workspace. An empty dest resolves to the workspace root.
func resolveDest(workspaceDir, dest string) (string, error) {
	if dest == "" {
		return workspaceDir, nil
	}
	if filepath.IsAbs(dest) {
		return "", fmt.Errorf("dest %q must be a relative path", dest)
	}
	resolved := filepath.Clean(filepath.Join(workspaceDir, dest))
	rel, err := filepath.Rel(workspaceDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("dest %q escapes the workspace", dest)
	}
	return resolved, nil
}

// gitWorktree wraps a `git worktree add` materialization so it can be
// cleaned up with `git worktree remove`.
type gitWorktree struct {
	sourceDir string
	target    string
	cleaned   bool
}

// gitWorktreeAdd runs `git worktree add --detach <target> [commit]` against
// the source repository. Using `--detach` lets us check out branch names
// (e.g. "main") without conflicting with the same branch already being
// checked out in the source repo.
func gitWorktreeAdd(ctx context.Context, sourceDir, commit, target string) (*gitWorktree, error) {
	if sourceDir == "" {
		return nil, fmt.Errorf("source is required")
	}
	absSource, err := filepath.Abs(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("resolving source path: %w", err)
	}

	args := []string{"worktree", "add", "--detach", target}
	if commit != "" {
		args = append(args, commit)
	}

	if _, err := runGit(ctx, absSource, args...); err != nil {
		return nil, fmt.Errorf("git worktree add failed: %w", err)
	}

	return &gitWorktree{sourceDir: absSource, target: target}, nil
}

func (g *gitWorktree) Cleanup(ctx context.Context) error {
	if g == nil || g.cleaned {
		return nil
	}
	g.cleaned = true
	// --force handles the case where the worktree has uncommitted changes
	// or git considers the target locked for any reason.
	if _, err := runGit(ctx, g.sourceDir, "worktree", "remove", "--force", g.target); err != nil {
		return fmt.Errorf("git worktree remove failed: %w", err)
	}
	return nil
}

// runGit executes a git command and returns its stdout on success, or an
// error including the trimmed stderr text on failure.
func runGit(ctx context.Context, repoDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}

// cleanupAll runs Cleanup on each resource, logging (but not returning)
// per-resource errors. Used to roll back partial setups.
func cleanupAll(ctx context.Context, resources []GitResource) {
	for _, r := range resources {
		if err := r.Cleanup(ctx); err != nil {
			slog.Warn("failed to cleanup partial git resource", "error", err)
		}
	}
}
