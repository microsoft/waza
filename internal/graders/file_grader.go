package graders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spboyer/waza/internal/models"
)

// errFileGraderNoChecks is the error format string returned when a file grader is created without any checks.
const errFileGraderNoChecks = "file grader '%s' must have at least one of 'must_exist', 'must_not_exist', or 'content_patterns'"

// FileContentPattern defines regex patterns to match against a file's content
type FileContentPattern struct {
	Path         string   // Path to file (relative to workspace)
	MustMatch    []string // Regex patterns that must match
	MustNotMatch []string // Regex patterns that must not match
}

// FileGraderArgs holds the arguments for creating a file grader.
type FileGraderArgs struct {
	// Name is the identifier for this grader, used in results and error messages.
	Name string
	// MustExist lists file paths (relative to workspace) that must be present.
	MustExist []string
	// MustNotExist lists file paths (relative to workspace) that must be absent.
	MustNotExist []string
	// ContentPatterns defines regex patterns to match against file contents.
	ContentPatterns []FileContentPattern
}

// fileGrader validates file existence and content patterns
type fileGrader struct {
	name            string
	mustExist       []string
	mustNotExist    []string
	contentPatterns []FileContentPattern
}

// NewFileGrader creates a [fileGrader], which can be used to perform simple existence (or non-existence) checks with
// 'mustExist'/'mustNotExist', or validate that the contents of the file match or do not match certain
// regex patterns, using 'contentPatterns'.
func NewFileGrader(args FileGraderArgs) (*fileGrader, error) {
	if len(args.MustExist) == 0 && len(args.MustNotExist) == 0 && len(args.ContentPatterns) == 0 {
		return nil, fmt.Errorf(errFileGraderNoChecks, args.Name)
	}

	return &fileGrader{
		name:            args.Name,
		mustExist:       args.MustExist,
		mustNotExist:    args.MustNotExist,
		contentPatterns: args.ContentPatterns,
	}, nil
}

func (fg *fileGrader) Name() string { return fg.name }
func (fg *fileGrader) Type() Type   { return TypeFile }

func (fg *fileGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		workspaceDir := gradingContext.WorkspaceDir
		if workspaceDir == "" {
			return &models.GraderResults{
				Name:     fg.name,
				Type:     string(TypeFile),
				Score:    0.0,
				Passed:   false,
				Feedback: "No workspace directory available for file grading",
			}, nil
		}

		var failures []string

		if err := fg.validateAllPaths(workspaceDir); err != nil {
			return nil, err
		}

		failures = append(failures, fg.checkMustExist(workspaceDir)...)
		failures = append(failures, fg.checkMustNotExist(workspaceDir)...)
		failures = append(failures, fg.checkContentPatterns(workspaceDir)...)

		return fg.buildResult(failures, workspaceDir), nil
	})
}

// checkMustExist verifies that all required files are present in the workspace.
func (fg *fileGrader) checkMustExist(workspaceDir string) []string {
	var failures []string
	for _, relPath := range fg.mustExist {
		fullPath := filepath.Join(workspaceDir, relPath)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("File must exist but not found: %s", relPath))
		}
	}
	return failures
}

// checkMustNotExist verifies that forbidden files are absent from the workspace.
func (fg *fileGrader) checkMustNotExist(workspaceDir string) []string {
	var failures []string
	for _, relPath := range fg.mustNotExist {
		fullPath := filepath.Join(workspaceDir, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			failures = append(failures, fmt.Sprintf("File must not exist but found: %s", relPath))
		}
	}
	return failures
}

// checkContentPatterns validates file contents against must_match and must_not_match regex patterns.
func (fg *fileGrader) checkContentPatterns(workspaceDir string) []string {
	var failures []string
	for _, cp := range fg.contentPatterns {
		fullPath := filepath.Join(workspaceDir, cp.Path)

		content, err := os.ReadFile(fullPath)
		if err != nil {
			failures = append(failures, fileReadFailures(cp, err)...)
			continue
		}

		contentStr := string(content)
		failures = append(failures, matchRegexPatterns(contentStr, cp.Path, cp.MustMatch, true)...)
		failures = append(failures, matchRegexPatterns(contentStr, cp.Path, cp.MustNotMatch, false)...)
	}
	return failures
}

// fileReadFailures returns failure messages when a file required for content checking cannot be read.
func fileReadFailures(contentPattern FileContentPattern, err error) []string {
	var failures []string

	if os.IsNotExist(err) {
		failures = append(failures, fmt.Sprintf("File not found for content check: %s", contentPattern.Path))
	} else {
		failures = append(failures, fmt.Sprintf("Failed to read file %s: %v", contentPattern.Path, err))
	}

	// if the file can't be read/found then we'll just automatically count all
	// the expected patterns as failures. This'll keep the total checks consistent between runs.
	for _, pattern := range contentPattern.MustMatch {
		failures = append(failures, fmt.Sprintf("File %s missing expected pattern (file not found): %s", contentPattern.Path, pattern))
	}

	for _, pattern := range contentPattern.MustNotMatch {
		failures = append(failures, fmt.Sprintf("File %s could not verify absence of pattern (file not found): %s", contentPattern.Path, pattern))
	}

	return failures
}

// matchRegexPatterns checks content against a list of regex patterns. When mustMatch is true,
// the content is expected to match each pattern; when false, it must not match.
func matchRegexPatterns(content, filePath string, regexPatterns []string, mustMatch bool) []string {
	var failures []string
	for _, pattern := range regexPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			kind := "must_match"
			if !mustMatch {
				kind = "must_not_match"
			}
			failures = append(failures, fmt.Sprintf("Invalid '%s' regex pattern %q for file %s: %v", kind, pattern, filePath, err))
			continue
		}

		matched := re.MatchString(content)
		if mustMatch && !matched {
			failures = append(failures, fmt.Sprintf("File %s missing expected pattern: %s", filePath, pattern))
		} else if !mustMatch && matched {
			failures = append(failures, fmt.Sprintf("File %s contains forbidden pattern: %s", filePath, pattern))
		}
	}
	return failures
}

// validatePathInWorkspace resolves the given relative path against workspaceDir and
// returns an error if the result escapes the workspace (e.g. via ".." traversal).
//
// NOTE: this isn't for security (users control the entire file, including all things that are written), but more
// to keep people from accidentally adding in hardcoded paths, or paths to files that are outside of the
// sandbox (ie, their tests would be considered dirty).
func validatePathInWorkspace(workspaceDir, relPath string) error {
	absWorkspace, err := filepath.Abs(workspaceDir)
	if err != nil {
		return fmt.Errorf("failed to resolve workspace dir: %w", err)
	}

	// Reject absolute paths outright — all paths should be relative to the workspace.
	if filepath.IsAbs(relPath) {
		return fmt.Errorf("path %q is absolute and not relative to workspace %q", relPath, absWorkspace)
	}

	fullPath := filepath.Join(absWorkspace, relPath)
	absFull, err := filepath.Abs(fullPath)
	if err != nil {
		return fmt.Errorf("failed to resolve path %q: %w", relPath, err)
	}

	// Ensure the resolved path is within the workspace by checking the prefix.
	// The trailing separator prevents "/workspace-foo" matching "/workspace".
	if !strings.HasPrefix(absFull, absWorkspace+string(filepath.Separator)) {
		return fmt.Errorf("path %q resolves to %q which is outside workspace %q", relPath, absFull, absWorkspace)
	}

	return nil
}

// validateAllPaths checks that every configured file path stays within the workspace.
func (fg *fileGrader) validateAllPaths(workspaceDir string) error {
	for _, p := range fg.mustExist {
		if err := validatePathInWorkspace(workspaceDir, p); err != nil {
			return err
		}
	}
	for _, p := range fg.mustNotExist {
		if err := validatePathInWorkspace(workspaceDir, p); err != nil {
			return err
		}
	}
	for _, cp := range fg.contentPatterns {
		if err := validatePathInWorkspace(workspaceDir, cp.Path); err != nil {
			return err
		}
	}
	return nil
}

// countTotalChecks returns the total number of individual checks to be performed.
func (fg *fileGrader) countTotalChecks() int {
	// files existing
	total := len(fg.mustExist) + len(fg.mustNotExist)

	// patterns per file
	for _, cp := range fg.contentPatterns {
		total += len(cp.MustMatch) + len(cp.MustNotMatch)

		// Add 1 for the file existence check implicit in content patterns
		if len(cp.MustMatch) > 0 || len(cp.MustNotMatch) > 0 {
			total++
		}
	}
	return total
}

// buildResult constructs the final GraderResults from the collected failures.
func (fg *fileGrader) buildResult(failures []string, workspaceDir string) *models.GraderResults {
	totalChecks := fg.countTotalChecks()
	passedChecks := totalChecks - len(failures)

	score := 1.0
	if totalChecks > 0 {
		score = float64(passedChecks) / float64(totalChecks)
	}

	feedback := "All file checks passed"
	if len(failures) > 0 {
		feedback = strings.Join(failures, "; ")
	}

	return &models.GraderResults{
		Name:     fg.name,
		Type:     string(TypeFile),
		Score:    score,
		Passed:   len(failures) == 0,
		Feedback: feedback,
		Details: map[string]any{
			"must_exist":       fg.mustExist,
			"must_not_exist":   fg.mustNotExist,
			"content_patterns": fg.contentPatterns,
			"failures":         failures,
			"workspace_dir":    workspaceDir,
		},
	}
}
