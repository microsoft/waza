package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrFileNotFound is returned when a file does not exist at the given ref.
var ErrFileNotFound = errors.New("file not found at ref")

// WorkingTreeRef is a sentinel value representing the working tree (not a git ref).
const WorkingTreeRef = "WORKING"

// IsInRepo returns true if dir is inside a git repository.
func IsInRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// GetFilesFromRef returns a list of markdown files tracked by git at a given ref.
// For [WorkingTreeRef], it returns tracked and untracked *.md[x] files (respecting
// .gitignore)
func GetFilesFromRef(dir, ref string) ([]string, error) {
	var cmd *exec.Cmd
	if ref == WorkingTreeRef {
		// --cached: tracked files, --others: untracked files,
		// --exclude-standard: respect .gitignore
		cmd = exec.Command("git", "-C", dir, "ls-files",
			"--cached", "--others", "--exclude-standard")
	} else {
		// git ls-tree doesn't support the same glob patterns, so list all files and filter
		cmd = exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", ref)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing files at %q: %w", ref, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var result []string
	for _, l := range lines {
		if l == "" {
			continue
		}
		lower := strings.ToLower(l)
		if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".mdx") {
			result = append(result, l)
		}
	}
	return result, nil
}

// RefExists returns true if the given git ref can be resolved.
func RefExists(dir, ref string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}

// GetFileFromRef retrieves the content of a file at a given git ref.
// The file path is resolved relative to the repository root, regardless
// of the working directory. Use [GetCWDFileFromRef] when the path comes
// from a CWD-relative listing such as git ls-tree run from a subdirectory.
// It returns [ErrFileNotFound] (wrapped) when the path does not exist at the
// given ref, so callers can distinguish "missing file" from other git errors.
func GetFileFromRef(dir, file, ref string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "show", ref+":"+file)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "does not exist") {
				return "", fmt.Errorf("reading %q at %s: %w", file, ref, ErrFileNotFound)
			}
		}
		return "", fmt.Errorf("reading %q at %s: %w", file, ref, err)
	}
	return string(out), nil
}

// GetCWDFileFromRef retrieves the content of a file at a given git ref,
// resolving the path relative to dir (the working directory) rather than the
// repository root. This matches how [GetFilesFromRef] returns CWD-relative
// paths from git ls-tree when run from a subdirectory.
func GetCWDFileFromRef(dir, file, ref string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "show", ref+":./"+file)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "does not exist") {
				return "", fmt.Errorf("reading %q at %s: %w", file, ref, ErrFileNotFound)
			}
		}
		return "", fmt.Errorf("reading %q at %s: %w", file, ref, err)
	}
	return string(out), nil
}
