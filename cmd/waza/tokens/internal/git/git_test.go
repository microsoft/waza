package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", fullPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", fullPath, err)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	return dir
}

func commitAll(t *testing.T, dir, message string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func TestIsInRepoAndRefExists(t *testing.T) {
	nonRepoDir := t.TempDir()
	if IsInRepo(nonRepoDir) {
		t.Fatalf("expected %q to not be a git repo", nonRepoDir)
	}

	repo := initTestRepo(t)
	writeFile(t, repo, "README.md", "hello")
	_ = commitAll(t, repo, "initial")

	if !IsInRepo(repo) {
		t.Fatalf("expected %q to be a git repo", repo)
	}
	if !RefExists(repo, "HEAD") {
		t.Fatalf("expected HEAD to exist")
	}
	if RefExists(repo, "refs/heads/does-not-exist") {
		t.Fatalf("expected missing ref to not exist")
	}
}

func TestGetFilesFromRefWorkingTreeAndRefHistory(t *testing.T) {
	repo := initTestRepo(t)

	writeFile(t, repo, ".gitignore", "ignored.md\n")
	writeFile(t, repo, "docs/Tracked.MDX", "tracked")
	writeFile(t, repo, "notes.md", "first")
	writeFile(t, repo, "notes.txt", "ignore me")
	firstRef := commitAll(t, repo, "first")

	writeFile(t, repo, "notes.md", "second")
	writeFile(t, repo, "second.mdx", "second")
	runGit(t, repo, "add", "notes.md", "second.mdx")
	runGit(t, repo, "rm", "docs/Tracked.MDX")
	secondRef := commitAll(t, repo, "second")

	writeFile(t, repo, "draft.md", "untracked")
	writeFile(t, repo, "ignored.md", "should be ignored")

	workingFiles, err := GetFilesFromRef(repo, WorkingTreeRef)
	if err != nil {
		t.Fatalf("GetFilesFromRef working tree: %v", err)
	}
	slices.Sort(workingFiles)
	expectedWorking := []string{"draft.md", "notes.md", "second.mdx"}
	if !slices.Equal(workingFiles, expectedWorking) {
		t.Fatalf("working tree files mismatch\nwant: %v\ngot:  %v", expectedWorking, workingFiles)
	}

	firstFiles, err := GetFilesFromRef(repo, firstRef)
	if err != nil {
		t.Fatalf("GetFilesFromRef first ref: %v", err)
	}
	slices.Sort(firstFiles)
	expectedFirst := []string{"docs/Tracked.MDX", "notes.md"}
	if !slices.Equal(firstFiles, expectedFirst) {
		t.Fatalf("first ref files mismatch\nwant: %v\ngot:  %v", expectedFirst, firstFiles)
	}

	secondFiles, err := GetFilesFromRef(repo, secondRef)
	if err != nil {
		t.Fatalf("GetFilesFromRef second ref: %v", err)
	}
	slices.Sort(secondFiles)
	expectedSecond := []string{"notes.md", "second.mdx"}
	if !slices.Equal(secondFiles, expectedSecond) {
		t.Fatalf("second ref files mismatch\nwant: %v\ngot:  %v", expectedSecond, secondFiles)
	}
}

func TestGetFilesFromRefInvalidRef(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "readme.md", "hello")
	_ = commitAll(t, repo, "initial")

	_, err := GetFilesFromRef(repo, "not-a-ref")
	if err == nil {
		t.Fatalf("expected error for invalid ref")
	}
	if !strings.Contains(err.Error(), "listing files at \"not-a-ref\"") {
		t.Fatalf("expected wrapped listing error, got: %v", err)
	}
}

func TestGetFileFromRefAndFileHistory(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "docs/history.md", "v1\n")
	firstRef := commitAll(t, repo, "first")

	writeFile(t, repo, "docs/history.md", "v2\n")
	secondRef := commitAll(t, repo, "second")

	firstContent, err := GetFileFromRef(repo, "docs/history.md", firstRef)
	if err != nil {
		t.Fatalf("GetFileFromRef first ref: %v", err)
	}
	if firstContent != "v1\n" {
		t.Fatalf("expected v1 content, got %q", firstContent)
	}

	secondContent, err := GetFileFromRef(repo, "docs/history.md", secondRef)
	if err != nil {
		t.Fatalf("GetFileFromRef second ref: %v", err)
	}
	if secondContent != "v2\n" {
		t.Fatalf("expected v2 content, got %q", secondContent)
	}
}

func TestGetFileFromRefNotFoundAndOtherErrors(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "exists.md", "ok")
	_ = commitAll(t, repo, "initial")

	_, err := GetFileFromRef(repo, "missing.md", "HEAD")
	if err == nil {
		t.Fatalf("expected missing file error")
	}
	if !errors.Is(err, ErrFileNotFound) {
		t.Fatalf("expected ErrFileNotFound, got: %v", err)
	}

	_, err = GetFileFromRef(repo, "exists.md", "not-a-ref")
	if err == nil {
		t.Fatalf("expected invalid ref error")
	}
	if errors.Is(err, ErrFileNotFound) {
		t.Fatalf("did not expect ErrFileNotFound for invalid ref")
	}
}

func TestGetCWDFileFromRef(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "root.md", "root content\n")
	writeFile(t, repo, "sub/nested.md", "nested content\n")
	_ = commitAll(t, repo, "initial")

	t.Run("from repo root", func(t *testing.T) {
		content, err := GetCWDFileFromRef(repo, "sub/nested.md", "HEAD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "nested content\n" {
			t.Fatalf("expected nested content, got %q", content)
		}
	})

	t.Run("from subdirectory", func(t *testing.T) {
		subDir := filepath.Join(repo, "sub")
		// GetCWDFileFromRef resolves paths relative to dir, so "nested.md"
		// from the sub/ directory should find sub/nested.md in the tree.
		content, err := GetCWDFileFromRef(subDir, "nested.md", "HEAD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "nested content\n" {
			t.Fatalf("expected nested content, got %q", content)
		}
	})

	t.Run("repo-root file not visible from subdirectory", func(t *testing.T) {
		subDir := filepath.Join(repo, "sub")
		// "root.md" exists at the repo root but not under sub/,
		// so GetCWDFileFromRef from sub/ should not find it.
		if _, err := GetCWDFileFromRef(subDir, "root.md", "HEAD"); !errors.Is(err, ErrFileNotFound) {
			t.Fatalf("expected ErrFileNotFound for root.md from subdirectory, got %v", err)
		}
	})

	t.Run("GetFileFromRef still uses repo-root paths", func(t *testing.T) {
		subDir := filepath.Join(repo, "sub")
		// GetFileFromRef always resolves relative to repo root,
		// so "root.md" should be found even when dir is a subdirectory.
		content, err := GetFileFromRef(subDir, "root.md", "HEAD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if content != "root content\n" {
			t.Fatalf("expected root content, got %q", content)
		}
	})
}
