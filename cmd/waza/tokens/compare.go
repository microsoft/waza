package tokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spboyer/waza/cmd/waza/tokens/internal"
	"github.com/spboyer/waza/cmd/waza/tokens/internal/git"
	"github.com/spboyer/waza/cmd/waza/tokens/internal/tokens"
	"github.com/spf13/cobra"
)

func newCompareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare [refs...]",
		Short: "Compare markdown tokens between git refs",
		Long: `Compare markdown token counts between git refs.

With no arguments, compares HEAD to the working tree.
With one ref, compares that ref to the working tree.
With two refs, compares the first ref to the second.`,
		Args:          cobra.MaximumNArgs(2),
		RunE:          runCompare,
		SilenceErrors: true,
	}
	cmd.Flags().String("format", "table", "Output format: json | table")
	cmd.Flags().Bool("show-unchanged", false, "Include unchanged files in output")
	return cmd
}

type fileTokens struct {
	Tokens     int `json:"tokens"`
	Characters int `json:"characters"`
	Lines      int `json:"lines"`
}

type fileComparison struct {
	File          string      `json:"file"`
	Before        *fileTokens `json:"before"`
	After         *fileTokens `json:"after"`
	Diff          int         `json:"diff"`
	PercentChange float64     `json:"percentChange"`
	Status        string      `json:"status"`
}

type comparisonSummary struct {
	TotalBefore    int     `json:"totalBefore"`
	TotalAfter     int     `json:"totalAfter"`
	TotalDiff      int     `json:"totalDiff"`
	PercentChange  float64 `json:"percentChange"`
	FilesAdded     int     `json:"filesAdded"`
	FilesRemoved   int     `json:"filesRemoved"`
	FilesModified  int     `json:"filesModified"`
	FilesIncreased int     `json:"filesIncreased"`
	FilesDecreased int     `json:"filesDecreased"`
}

type comparisonReport struct {
	BaseRef   string            `json:"baseRef"`
	HeadRef   string            `json:"headRef"`
	Timestamp string            `json:"timestamp"`
	Summary   comparisonSummary `json:"summary"`
	Files     []fileComparison  `json:"files"`
}

func runCompare(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	if format != "table" && format != "json" {
		return fmt.Errorf(`unsupported format %q; expected "table" or "json"`, format)
	}
	showUnchanged, err := cmd.Flags().GetBool("show-unchanged")
	if err != nil {
		return err
	}

	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	if !git.IsInRepo(rootDir) {
		return fmt.Errorf("not a git repository; compare command requires git")
	}

	var baseRef, headRef string
	switch len(args) {
	case 0:
		baseRef = "HEAD"
		headRef = git.WorkingTreeRef
	case 1:
		baseRef = args[0]
		headRef = git.WorkingTreeRef
	default:
		baseRef = args[0]
		headRef = args[1]
	}

	comparisons, err := compareRefs(baseRef, headRef, rootDir)
	if err != nil {
		return err
	}
	// summarize before filtering out unchanged files so totals reflect all files, not just changed ones
	summary := calculateSummary(comparisons)
	if !showUnchanged {
		comparisons = slices.DeleteFunc(comparisons, func(c fileComparison) bool {
			return c.Status == "unchanged"
		})
	}
	// Sort: changed first, then alphabetical
	sort.Slice(comparisons, func(i, j int) bool {
		ci := comparisons[i].Status != "unchanged"
		cj := comparisons[j].Status != "unchanged"
		if ci != cj {
			return ci
		}
		return comparisons[i].File < comparisons[j].File
	})

	out := cmd.OutOrStdout()
	if format == "json" {
		s, err := compareJSON(comparisons, summary, baseRef, headRef)
		if err != nil {
			return err
		}
		fmt.Fprint(out, s)
		return nil
	}
	fmt.Fprint(out, compareTable(comparisons, summary, baseRef, headRef))
	return nil
}

// listRefFiles returns the set of markdown files present at a git ref.
// If the ref cannot be resolved or the listing fails for any reason, an
// empty set is returned so that the comparison can proceed gracefully
// (matching the former TypeScript implementation's behavior).
func listRefFiles(dir, ref string) map[string]bool {
	files := make(map[string]bool)
	list, err := git.GetFilesFromRef(dir, ref)
	if err != nil {
		return files
	}
	for _, f := range list {
		files[f] = true
	}
	return files
}

func compareRefs(baseRef, headRef, rootDir string) ([]fileComparison, error) {
	counter := tokens.NewEstimatingCounter()

	baseFiles := listRefFiles(rootDir, baseRef)
	headFiles := listRefFiles(rootDir, headRef)

	allFiles := make(map[string]bool)
	for f := range baseFiles {
		allFiles[f] = true
	}
	for f := range headFiles {
		allFiles[f] = true
	}

	var comparisons []fileComparison
	for file := range allFiles {
		var baseContent string
		var hasBase bool
		if baseFiles[file] {
			var err error
			baseContent, err = git.GetFileFromRef(rootDir, file, baseRef)
			if err == nil {
				hasBase = true
			}
			// Any error (file not found, ref issues, etc.) is treated as
			// the file not existing at this ref, matching the TypeScript
			// implementation which returned null on any git error.
		}

		var headContent string
		var hasHead bool
		if headFiles[file] {
			if headRef == git.WorkingTreeRef {
				data, err := os.ReadFile(filepath.Join(rootDir, file))
				switch {
				case errors.Is(err, os.ErrNotExist):
					// File is tracked by git but deleted from working tree
				case err != nil:
					return nil, fmt.Errorf("reading %q: %w", file, err)
				default:
					headContent = string(data)
					hasHead = true
				}
			} else {
				var err error
				headContent, err = git.GetFileFromRef(rootDir, file, headRef)
				if err == nil {
					hasHead = true
				}
			}
		}

		beforeTokens := 0
		if hasBase {
			beforeTokens = counter.Count(baseContent)
		}
		afterTokens := 0
		if hasHead {
			afterTokens = counter.Count(headContent)
		}

		delta := afterTokens - beforeTokens
		var pctChange float64
		if beforeTokens > 0 {
			pctChange = float64(delta) / float64(beforeTokens) * 100
		} else if afterTokens > 0 {
			pctChange = 100
		}

		var status string
		switch {
		case !hasBase && hasHead:
			status = "added"
		case hasBase && !hasHead:
			status = "removed"
		case delta != 0:
			status = "modified"
		default:
			status = "unchanged"
		}

		var before *fileTokens
		if hasBase {
			before = &fileTokens{
				Tokens:     beforeTokens,
				Characters: utf8.RuneCountInString(baseContent),
				Lines:      countLines(baseContent),
			}
		}
		var after *fileTokens
		if hasHead {
			after = &fileTokens{
				Tokens:     afterTokens,
				Characters: utf8.RuneCountInString(headContent),
				Lines:      countLines(headContent),
			}
		}

		comparisons = append(comparisons, fileComparison{
			File:          internal.NormalizePath(file),
			Before:        before,
			After:         after,
			Diff:          delta,
			PercentChange: pctChange,
			Status:        status,
		})
	}

	return comparisons, nil
}

// countLines returns the number of lines in s. An empty string has 0 lines.
// A trailing newline does not count as an additional line (matches wc -l behavior
// for files that end with a newline).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func calculateSummary(comparisons []fileComparison) comparisonSummary {
	var s comparisonSummary
	for _, c := range comparisons {
		if c.Before != nil {
			s.TotalBefore += c.Before.Tokens
		}
		if c.After != nil {
			s.TotalAfter += c.After.Tokens
		}
		switch c.Status {
		case "added":
			s.FilesAdded++
		case "removed":
			s.FilesRemoved++
		case "modified":
			s.FilesModified++
		}
		if c.Diff > 0 {
			s.FilesIncreased++
		} else if c.Diff < 0 {
			s.FilesDecreased++
		}
	}
	s.TotalDiff = s.TotalAfter - s.TotalBefore
	if s.TotalBefore > 0 {
		s.PercentChange = float64(s.TotalDiff) / float64(s.TotalBefore) * 100
	} else if s.TotalAfter > 0 {
		s.PercentChange = 100
	}
	return s
}

func compareTable(comparisons []fileComparison, summary comparisonSummary, baseRef, headRef string) string {
	var sb strings.Builder

	if len(comparisons) == 0 {
		sb.WriteString("No changes detected.\n")
		return sb.String()
	}

	fmt.Fprintf(&sb, "\n📊 Token Comparison: %s → %s\n\n", baseRef, headRef)

	maxPath := 4
	for _, c := range comparisons {
		if len(c.File) > maxPath {
			maxPath = len(c.File)
		}
	}

	header := fmt.Sprintf("%-*s  %8s  %8s  %8s  Status", maxPath, "File", "Before", "After", "Diff")
	sb.WriteString(header + "\n")
	sb.WriteString(strings.Repeat("-", len(header)+10) + "\n")

	for _, c := range comparisons {
		before := "-"
		if c.Before != nil {
			before = fmt.Sprintf("%d", c.Before.Tokens)
		}
		after := "-"
		if c.After != nil {
			after = fmt.Sprintf("%d", c.After.Tokens)
		}
		diffStr := fmt.Sprintf("%d", c.Diff)
		if c.Diff > 0 {
			diffStr = fmt.Sprintf("+%d", c.Diff)
		}

		var statusIcon string
		switch c.Status {
		case "added":
			statusIcon = "🆕"
		case "removed":
			statusIcon = "🗑️"
		case "modified":
			if c.Diff > 0 {
				statusIcon = "📈"
			} else {
				statusIcon = "📉"
			}
		case "unchanged":
			statusIcon = "➡️"
		}

		fmt.Fprintf(&sb, "%-*s  %8s  %8s  %8s  %s\n", maxPath, c.File, before, after, diffStr, statusIcon)
	}

	sb.WriteString(strings.Repeat("-", len(header)+10) + "\n")

	totalDiffStr := fmt.Sprintf("%d", summary.TotalDiff)
	if summary.TotalDiff > 0 {
		totalDiffStr = fmt.Sprintf("+%d", summary.TotalDiff)
	}
	fmt.Fprintf(&sb, "%-*s  %8d  %8d  %8s  %.1f%%\n", maxPath, "Total",
		summary.TotalBefore, summary.TotalAfter, totalDiffStr, summary.PercentChange)

	fmt.Fprintf(&sb, "\n📋 Summary:\n")
	fmt.Fprintf(&sb, "   Added: %d, Removed: %d, Modified: %d\n", summary.FilesAdded, summary.FilesRemoved, summary.FilesModified)
	fmt.Fprintf(&sb, "   Increased: %d, Decreased: %d\n", summary.FilesIncreased, summary.FilesDecreased)

	return sb.String()
}

func compareJSON(comparisons []fileComparison, summary comparisonSummary, baseRef, headRef string) (string, error) {
	report := comparisonReport{
		BaseRef:   baseRef,
		HeadRef:   headRef,
		Timestamp: nowISO(),
		Summary:   summary,
		Files:     comparisons,
	}

	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return "", err
	}
	return sb.String(), nil
}
