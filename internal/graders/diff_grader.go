package graders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spboyer/waza/internal/models"
)

// errDiffGraderNoChecks is the error format string returned when a diff grader has no expected files.
const errDiffGraderNoChecks = "diff grader '%s' must have at least one expected_files entry"

// ExpectedFile defines a single file expectation for the diff grader.
// Either Snapshot or Contains (or both) must be specified.
type ExpectedFile struct {
	// Path is the workspace-relative path to the file being checked.
	Path string
	// Snapshot is the path (relative to context/fixtures dir) of the expected file content.
	// When set, the workspace file must match this snapshot exactly.
	Snapshot string
	// Contains lists line fragments that must appear in the workspace file.
	// Prefixed with "+" means the line must be present; "-" means it must be absent.
	Contains []string
}

// DiffGraderArgs holds the arguments for creating a diff grader.
type DiffGraderArgs struct {
	// Name is the identifier for this grader, used in results and error messages.
	Name string
	// ExpectedFiles lists the file expectations to check against the workspace.
	ExpectedFiles []ExpectedFile
	// ContextDir is the base directory for resolving snapshot paths.
	ContextDir string
}

// diffGrader compares post-execution workspace files against expected snapshots and diff fragments.
type diffGrader struct {
	name          string
	expectedFiles []ExpectedFile
	contextDir    string
}

// NewDiffGrader creates a [diffGrader] that validates workspace files against expected
// snapshots (exact match) and/or contains-line fragments (must appear / must not appear).
func NewDiffGrader(args DiffGraderArgs) (*diffGrader, error) {
	if len(args.ExpectedFiles) == 0 {
		return nil, fmt.Errorf(errDiffGraderNoChecks, args.Name)
	}

	for i, ef := range args.ExpectedFiles {
		if ef.Path == "" {
			return nil, fmt.Errorf("diff grader '%s': expected_files[%d] missing required 'path'", args.Name, i)
		}
		if ef.Snapshot == "" && len(ef.Contains) == 0 {
			return nil, fmt.Errorf("diff grader '%s': expected_files[%d] ('%s') must have 'snapshot' or 'contains'", args.Name, i, ef.Path)
		}
	}

	return &diffGrader{
		name:          args.Name,
		expectedFiles: args.ExpectedFiles,
		contextDir:    args.ContextDir,
	}, nil
}

func (dg *diffGrader) Name() string            { return dg.name }
func (dg *diffGrader) Kind() models.GraderKind { return models.GraderKindDiff }

func (dg *diffGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		workspaceDir := gradingContext.WorkspaceDir
		if workspaceDir == "" {
			return &models.GraderResults{
				Name:     dg.name,
				Type:     models.GraderKindDiff,
				Score:    0.0,
				Passed:   false,
				Feedback: "No workspace directory available for diff grading",
			}, nil
		}

		if err := dg.validateAllPaths(workspaceDir); err != nil {
			return nil, err
		}

		var failures []string
		for _, ef := range dg.expectedFiles {
			failures = append(failures, dg.checkExpectedFile(workspaceDir, ef)...)
		}

		return dg.buildResult(failures, workspaceDir), nil
	})
}

// checkExpectedFile validates a single expected file against the workspace.
func (dg *diffGrader) checkExpectedFile(workspaceDir string, ef ExpectedFile) []string {
	var failures []string

	fullPath := filepath.Join(workspaceDir, ef.Path)
	actualContent, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("Expected file not found in workspace: %s", ef.Path))
		} else {
			failures = append(failures, fmt.Sprintf("Failed to read workspace file %s: %v", ef.Path, err))
		}
		// Count all sub-checks as failures when file is unreadable
		if ef.Snapshot != "" {
			failures = append(failures, fmt.Sprintf("Snapshot comparison skipped (file not found): %s", ef.Path))
		}
		for _, c := range ef.Contains {
			failures = append(failures, fmt.Sprintf("Contains check skipped (file not found): %s → %s", ef.Path, c))
		}
		return failures
	}

	if ef.Snapshot != "" {
		failures = append(failures, dg.checkSnapshot(ef, string(actualContent))...)
	}

	if len(ef.Contains) > 0 {
		failures = append(failures, dg.checkContains(ef, string(actualContent))...)
	}

	return failures
}

// checkSnapshot compares workspace file content against the expected snapshot file.
func (dg *diffGrader) checkSnapshot(ef ExpectedFile, actualContent string) []string {
	snapshotPath := ef.Snapshot
	if dg.contextDir != "" && !filepath.IsAbs(snapshotPath) {
		snapshotPath = filepath.Join(dg.contextDir, snapshotPath)
	}

	expectedContent, err := os.ReadFile(snapshotPath)
	if err != nil {
		return []string{fmt.Sprintf("Failed to read snapshot file %s for %s: %v", ef.Snapshot, ef.Path, err)}
	}

	if actualContent != string(expectedContent) {
		return []string{fmt.Sprintf("File %s does not match snapshot %s", ef.Path, ef.Snapshot)}
	}

	return nil
}

// checkContains validates that required line fragments are present or absent in the file.
// Lines prefixed with "+" must appear; lines prefixed with "-" must not appear.
// Lines without a prefix are treated as must-appear.
func (dg *diffGrader) checkContains(ef ExpectedFile, actualContent string) []string {
	var failures []string

	for _, fragment := range ef.Contains {
		if fragment == "" {
			continue
		}

		mustBePresent := true
		checkStr := fragment

		switch fragment[0] {
		case '+':
			checkStr = fragment[1:]
		case '-':
			mustBePresent = false
			checkStr = fragment[1:]
		}

		checkStr = strings.TrimSpace(checkStr)
		if checkStr == "" {
			continue
		}

		found := strings.Contains(actualContent, checkStr)
		if mustBePresent && !found {
			failures = append(failures, fmt.Sprintf("File %s missing expected fragment: %s", ef.Path, checkStr))
		} else if !mustBePresent && found {
			failures = append(failures, fmt.Sprintf("File %s contains fragment that should be absent: %s", ef.Path, checkStr))
		}
	}

	return failures
}

// validateAllPaths checks that every configured file path stays within the workspace.
func (dg *diffGrader) validateAllPaths(workspaceDir string) error {
	for _, ef := range dg.expectedFiles {
		if err := validatePathInWorkspace(workspaceDir, ef.Path); err != nil {
			return err
		}
	}
	return nil
}

// countTotalChecks returns the total number of individual checks to be performed.
func (dg *diffGrader) countTotalChecks() int {
	total := 0
	for _, ef := range dg.expectedFiles {
		// File existence is always an implicit check
		total++
		if ef.Snapshot != "" {
			total++
		}
		total += len(ef.Contains)
	}
	return total
}

// buildResult constructs the final GraderResults from the collected failures.
func (dg *diffGrader) buildResult(failures []string, workspaceDir string) *models.GraderResults {
	totalChecks := dg.countTotalChecks()
	passedChecks := totalChecks - len(failures)

	score := 1.0
	if totalChecks > 0 {
		score = float64(passedChecks) / float64(totalChecks)
	}

	feedback := "All diff checks passed"
	if len(failures) > 0 {
		feedback = strings.Join(failures, "; ")
	}

	// Build per-file summary for details
	fileChecks := make([]map[string]any, 0, len(dg.expectedFiles))
	for _, ef := range dg.expectedFiles {
		entry := map[string]any{
			"path": ef.Path,
		}
		if ef.Snapshot != "" {
			entry["snapshot"] = ef.Snapshot
		}
		if len(ef.Contains) > 0 {
			entry["contains"] = ef.Contains
		}
		fileChecks = append(fileChecks, entry)
	}

	return &models.GraderResults{
		Name:     dg.name,
		Type:     models.GraderKindDiff,
		Score:    score,
		Passed:   len(failures) == 0,
		Feedback: feedback,
		Details: map[string]any{
			"expected_files": fileChecks,
			"failures":       failures,
			"workspace_dir":  workspaceDir,
		},
	}
}
