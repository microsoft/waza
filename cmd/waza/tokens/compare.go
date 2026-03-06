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

	"github.com/microsoft/waza/cmd/waza/tokens/internal/git"
	"github.com/microsoft/waza/internal/checks"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/tokens"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const defaultSkillsBaseRef = "origin/main"

func newCompareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare [refs...]",
		Short: "Compare markdown tokens between git refs",
		Long: `Compare markdown token counts between git refs.

With no arguments, compares HEAD to the working tree.
With one ref, compares that ref to the working tree.
With two refs, compares the first ref to the second.

Use --skills to restrict comparison to SKILL.md files under configured skill
roots (skills/, .github/skills/, and paths.skills from .waza.yaml). In skills
mode the default base ref is origin/main (falling back to main).

Use --threshold to set a percentage-change gate for CI. Files whose token
count increases by more than the threshold cause a non-zero exit. Newly added
files are exempt from threshold checks (no baseline to compare against) but
still subject to absolute limit checks when --strict is set.`,
		Args:          cobra.MaximumNArgs(2),
		RunE:          runCompare,
		SilenceErrors: true,
	}
	cmd.Flags().String("format", "table", "Output format: json | table")
	cmd.Flags().Bool("show-unchanged", false, "Include unchanged files in output")
	cmd.Flags().Bool("strict", false, "Exit with code 1 if any file exceeds its token limit")
	cmd.Flags().Bool("skills", false, "Only compare SKILL.md files under configured skill roots")
	cmd.Flags().Float64("threshold", 0, "Fail when any existing file increases by more than this percent (0 = disabled)")
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
	Limit         int         `json:"limit,omitempty"`
	OverLimit     bool        `json:"overLimit,omitempty"`
	Exceeded      bool        `json:"exceeded,omitempty"`
}

type comparisonSummary struct {
	TotalBefore       int     `json:"totalBefore"`
	TotalAfter        int     `json:"totalAfter"`
	TotalDiff         int     `json:"totalDiff"`
	PercentChange     float64 `json:"percentChange"`
	FilesAdded        int     `json:"filesAdded"`
	FilesRemoved      int     `json:"filesRemoved"`
	FilesModified     int     `json:"filesModified"`
	FilesIncreased    int     `json:"filesIncreased"`
	FilesDecreased    int     `json:"filesDecreased"`
	ExceededCount     int     `json:"exceededCount,omitempty"`
	LimitExceeded     int     `json:"limitExceeded,omitempty"`
	ThresholdBreached int     `json:"thresholdBreached,omitempty"`
}

type comparisonReport struct {
	BaseRef   string            `json:"baseRef"`
	HeadRef   string            `json:"headRef"`
	Threshold float64           `json:"threshold,omitempty"`
	Passed    bool              `json:"passed"`
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
	strict, err := cmd.Flags().GetBool("strict")
	if err != nil {
		return err
	}
	skillsOnly, err := cmd.Flags().GetBool("skills")
	if err != nil {
		return err
	}
	threshold, err := cmd.Flags().GetFloat64("threshold")
	if err != nil {
		return err
	}
	if threshold < 0 {
		return errors.New("threshold must be >= 0")
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
		if skillsOnly {
			baseRef = resolveSkillsBaseRef(rootDir)
		} else {
			baseRef = "HEAD"
		}
		headRef = git.WorkingTreeRef
	case 1:
		baseRef = args[0]
		headRef = git.WorkingTreeRef
	default:
		baseRef = args[0]
		headRef = args[1]
	}

	var comparisons []fileComparison
	if skillsOnly {
		comparisons, err = compareSkillRefs(baseRef, headRef, rootDir)
	} else {
		comparisons, err = compareRefs(baseRef, headRef, rootDir)
	}
	if err != nil {
		return err
	}

	// Load token limits when --strict or --threshold is set — both need limit awareness
	if strict || threshold > 0 {
		cfg := loadCompareLimitsConfig(rootDir)
		for i := range comparisons {
			if comparisons[i].After != nil {
				lr := checks.GetLimitForFile(comparisons[i].File, cfg)
				comparisons[i].Limit = lr.Limit
				comparisons[i].OverLimit = comparisons[i].After.Tokens > lr.Limit
				// Absolute limit breaches only cause failure when --strict is set
				if strict && comparisons[i].OverLimit {
					comparisons[i].Exceeded = true
				}
			}
		}
	}

	// Mark threshold breaches: only on existing files (not newly added)
	if threshold > 0 {
		for i := range comparisons {
			if comparisons[i].Diff > 0 && comparisons[i].Status != "added" && comparisons[i].PercentChange > threshold {
				comparisons[i].Exceeded = true
			}
		}
	}

	// summarize before filtering out unchanged files so totals reflect all files
	summary := calculateSummary(comparisons, threshold, strict)
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

	// Determine pass/fail: any exceeded file (limit or threshold) is a failure
	passed := summary.ExceededCount == 0

	out := cmd.OutOrStdout()
	if format == "json" {
		s, err := compareJSON(comparisons, summary, baseRef, headRef, threshold, passed)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, s); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	} else {
		if _, err := fmt.Fprint(out, compareTable(comparisons, summary, baseRef, headRef, threshold)); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	if !passed {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return buildFailureError(summary, threshold)
	}
	return nil
}

// resolveSkillsBaseRef returns the default base ref for --skills mode.
// Prefers origin/main, falls back to main.
func resolveSkillsBaseRef(rootDir string) string {
	if git.RefExists(rootDir, defaultSkillsBaseRef) {
		return defaultSkillsBaseRef
	}
	if git.RefExists(rootDir, "main") {
		return "main"
	}
	return "HEAD"
}

// buildFailureError produces a descriptive error message.
// LimitExceeded and ThresholdBreached are tracked independently so a file
// that triggers both is correctly reported in both categories.
func buildFailureError(summary comparisonSummary, threshold float64) error {
	var parts []string
	if summary.ThresholdBreached > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) exceeded %.1f%% threshold", summary.ThresholdBreached, threshold))
	}
	if summary.LimitExceeded > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) over absolute token limit", summary.LimitExceeded))
	}
	if len(parts) == 0 {
		return fmt.Errorf("%d file(s) exceed token limits after changes", summary.ExceededCount)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
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

// skillRootsForRef discovers skill directories for a given ref.
func skillRootsForRef(rootDir, ref string) []string {
	roots := []string{"skills", ".github/skills"}
	addRoot := func(v string) {
		v = strings.Trim(strings.TrimSpace(filepath.ToSlash(v)), "/")
		if v == "" {
			return
		}
		for _, existing := range roots {
			if existing == v {
				return
			}
		}
		roots = append(roots, v)
	}

	if ref == git.WorkingTreeRef {
		cfg, err := projectconfig.Load(rootDir)
		if err == nil {
			addRoot(cfg.Paths.Skills)
		}
		return roots
	}

	raw, err := git.GetFileFromRef(rootDir, ".waza.yaml", ref)
	if err != nil {
		return roots
	}
	var cfg struct {
		Paths struct {
			Skills string `yaml:"skills"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal([]byte(raw), &cfg); err == nil {
		addRoot(cfg.Paths.Skills)
	}
	return roots
}

// filterSkillFiles returns only SKILL.md files under the given roots.
func filterSkillFiles(files map[string]bool, roots []string) map[string]bool {
	filtered := make(map[string]bool)
	for file := range files {
		normalized := filepath.ToSlash(file)
		if !strings.EqualFold(filepath.Base(normalized), "SKILL.md") {
			continue
		}
		for _, root := range roots {
			root = strings.Trim(strings.TrimSpace(filepath.ToSlash(root)), "/")
			if root == "" {
				continue
			}
			if strings.HasPrefix(normalized, root+"/") {
				filtered[file] = true
				break
			}
		}
	}
	return filtered
}

// compareSkillRefs compares only SKILL.md files under configured skill roots.
func compareSkillRefs(baseRef, headRef, rootDir string) ([]fileComparison, error) {
	baseRoots := skillRootsForRef(rootDir, baseRef)
	headRoots := skillRootsForRef(rootDir, headRef)

	baseFiles := filterSkillFiles(listRefFiles(rootDir, baseRef), baseRoots)
	headFiles := filterSkillFiles(listRefFiles(rootDir, headRef), headRoots)

	return compareFilesets(baseRef, headRef, rootDir, baseFiles, headFiles)
}

func compareRefs(baseRef, headRef, rootDir string) ([]fileComparison, error) {
	baseFiles := listRefFiles(rootDir, baseRef)
	headFiles := listRefFiles(rootDir, headRef)
	return compareFilesets(baseRef, headRef, rootDir, baseFiles, headFiles)
}

// compareFilesets is the shared comparison engine for both modes.
func compareFilesets(baseRef, headRef, rootDir string, baseFiles, headFiles map[string]bool) ([]fileComparison, error) {
	counter, err := tokens.NewCounter(tokens.TokenizerDefault)
	if err != nil {
		return nil, err
	}

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
			File:          strings.ReplaceAll(file, `\`, "/"),
			Before:        before,
			After:         after,
			Diff:          delta,
			PercentChange: pctChange,
			Status:        status,
		})
	}

	return comparisons, nil
}

func calculateSummary(comparisons []fileComparison, threshold float64, strict bool) comparisonSummary {
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
		if c.Exceeded {
			s.ExceededCount++
		}
		// Track limit and threshold breaches independently
		if strict && c.OverLimit {
			s.LimitExceeded++
		}
		if threshold > 0 && c.Status != "added" && c.PercentChange > threshold {
			s.ThresholdBreached++
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

func compareTable(comparisons []fileComparison, summary comparisonSummary, baseRef, headRef string, threshold float64) string {
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
		switch {
		case c.OverLimit && c.Exceeded:
			statusIcon = fmt.Sprintf("⚠️ Over limit (%d)", c.Limit)
		case c.Exceeded:
			statusIcon = fmt.Sprintf("⚠️ +%.1f%%", c.PercentChange)
		case c.OverLimit:
			statusIcon = fmt.Sprintf("ℹ️ Over limit (%d)", c.Limit)
		case c.Status == "added":
			statusIcon = "🆕"
		case c.Status == "removed":
			statusIcon = "🗑️"
		case c.Status == "modified" && c.Diff > 0:
			statusIcon = "📈"
		case c.Status == "modified":
			statusIcon = "📉"
		case c.Status == "unchanged":
			statusIcon = "➡️"
		default:
			statusIcon = "🆕"
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

	if threshold > 0 {
		fmt.Fprintf(&sb, "   Threshold: %.1f%%\n", threshold)
	}

	if summary.ExceededCount > 0 {
		fmt.Fprintf(&sb, "\n⚠️  %d file(s) exceed limits:\n", summary.ExceededCount)
		for _, c := range comparisons {
			if c.Exceeded && c.After != nil {
				if c.OverLimit {
					over := c.After.Tokens - c.Limit
					fmt.Fprintf(&sb, "   %s: %d tokens (%d over limit of %d)\n", c.File, c.After.Tokens, over, c.Limit)
				} else {
					fmt.Fprintf(&sb, "   %s: +%.1f%% (threshold %.1f%%)\n", c.File, c.PercentChange, threshold)
				}
			}
		}
	}

	return sb.String()
}

func compareJSON(comparisons []fileComparison, summary comparisonSummary, baseRef, headRef string, threshold float64, passed bool) (string, error) {
	report := comparisonReport{
		BaseRef:   baseRef,
		HeadRef:   headRef,
		Threshold: threshold,
		Passed:    passed,
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

// loadCompareLimitsConfig loads token limits from .waza.yaml, falling back to
// .token-limits.json, then built-in defaults.
func loadCompareLimitsConfig(rootDir string) checks.TokenLimitsConfig {
	pcfg, err := projectconfig.Load(rootDir)
	if err == nil && pcfg.Tokens.Limits != nil && pcfg.Tokens.Limits.Defaults != nil {
		overrides := pcfg.Tokens.Limits.Overrides
		if overrides == nil {
			overrides = map[string]int{}
		}
		return checks.TokenLimitsConfig{
			Defaults:  pcfg.Tokens.Limits.Defaults,
			Overrides: overrides,
		}
	}
	cfg, err := checks.LoadLimitsConfig(rootDir)
	if err != nil {
		return checks.DefaultLimits
	}
	return cfg
}
