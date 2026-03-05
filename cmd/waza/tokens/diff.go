package tokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/waza/cmd/waza/tokens/internal/git"
	"github.com/microsoft/waza/internal/checks"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/tokens"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	defaultDiffBaseRef   = "origin/main"
	defaultDiffThreshold = 10.0
)

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff [base-ref]",
		Short: "Compare SKILL.md token budgets against a base ref",
		Long: `Compare token budgets for SKILL.md files between a base git ref and current changes.

By default, compares origin/main to the working tree and reports per-skill deltas.
Skill roots include configured paths.skills from .waza.yaml plus skills/ and
.github/skills/.`,
		Args:          cobra.MaximumNArgs(1),
		RunE:          runDiff,
		SilenceErrors: true,
	}
	cmd.Flags().String("format", "table", "Output format: table | json")
	cmd.Flags().Float64("threshold", defaultDiffThreshold, "Fail if any skill exceeds this percent increase")
	return cmd
}

type skillDiff struct {
	Skill             string  `json:"skill"`
	Path              string  `json:"path"`
	Before            int     `json:"before"`
	After             int     `json:"after"`
	Delta             int     `json:"delta"`
	PercentChange     float64 `json:"percentChange"`
	ThresholdExceeded bool    `json:"thresholdExceeded"`
	Limit             int     `json:"limit,omitempty"`
	OverLimit         bool    `json:"overLimit,omitempty"`
}

type diffSummary struct {
	SkillsCompared int `json:"skillsCompared"`
	TotalBefore    int `json:"totalBefore"`
	TotalAfter     int `json:"totalAfter"`
	TotalDelta     int `json:"totalDelta"`
}

type diffReport struct {
	BaseRef   string      `json:"baseRef"`
	HeadRef   string      `json:"headRef"`
	Threshold float64     `json:"threshold"`
	Passed    bool        `json:"passed"`
	Summary   diffSummary `json:"summary"`
	Skills    []skillDiff `json:"skills"`
}

func runDiff(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	if format != "table" && format != "json" {
		return fmt.Errorf(`unsupported format %q; expected "table" or "json"`, format)
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
		return errors.New("not a git repository; diff command requires git")
	}

	baseRef := defaultDiffBaseRef
	if len(args) == 1 {
		baseRef = args[0]
	} else if !git.RefExists(rootDir, baseRef) && git.RefExists(rootDir, "main") {
		baseRef = "main"
	}
	headRef := git.WorkingTreeRef

	limits := loadDiffLimitsConfig(rootDir)
	diffs, summary, err := compareSkillDiffs(rootDir, baseRef, headRef, threshold, limits)
	if err != nil {
		return err
	}
	passed := true
	for _, d := range diffs {
		if d.ThresholdExceeded {
			passed = false
			break
		}
	}

	if format == "json" {
		report := diffReport{
			BaseRef:   baseRef,
			HeadRef:   headRef,
			Threshold: threshold,
			Passed:    passed,
			Summary:   summary,
			Skills:    diffs,
		}
		var sb strings.Builder
		enc := json.NewEncoder(&sb)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if _, err := fmt.Fprint(cmd.OutOrStdout(), sb.String()); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	} else {
		if _, err := fmt.Fprint(cmd.OutOrStdout(), formatSkillDiffTable(baseRef, headRef, threshold, diffs, summary)); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	if !passed {
		cmd.SilenceUsage = true
		return fmt.Errorf("token diff threshold exceeded for one or more skills (threshold %.1f%%)", threshold)
	}
	return nil
}

func compareSkillDiffs(rootDir, baseRef, headRef string, threshold float64, limits checks.TokenLimitsConfig) ([]skillDiff, diffSummary, error) {
	baseRoots := skillRootsForRef(rootDir, baseRef)
	headRoots := skillRootsForRef(rootDir, headRef)

	baseSkills := collectSkillFilesForRef(rootDir, baseRef, baseRoots)
	headSkills := collectSkillFilesForRef(rootDir, headRef, headRoots)

	allKeys := make(map[string]struct{})
	for k := range baseSkills {
		allKeys[k] = struct{}{}
	}
	for k := range headSkills {
		allKeys[k] = struct{}{}
	}

	var keys []string
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	counter, err := tokens.NewCounter(tokens.TokenizerDefault)
	if err != nil {
		return nil, diffSummary{}, fmt.Errorf("failed to initialize token counter: %w", err)
	}

	diffs := make([]skillDiff, 0, len(keys))
	summary := diffSummary{SkillsCompared: len(keys)}
	for _, key := range keys {
		before := countSkillTokensAtRef(rootDir, baseRef, baseSkills[key], counter)
		after := countSkillTokensAtRef(rootDir, headRef, headSkills[key], counter)
		delta := after - before
		percent := percentageDiff(before, delta)
		thresholdExceeded := delta > 0 && percent > threshold
		limit := checks.GetLimitForFile(key, limits).Limit

		diffs = append(diffs, skillDiff{
			Skill:             filepath.Base(filepath.Dir(key)),
			Path:              filepath.ToSlash(filepath.Dir(key)),
			Before:            before,
			After:             after,
			Delta:             delta,
			PercentChange:     percent,
			ThresholdExceeded: thresholdExceeded,
			Limit:             limit,
			OverLimit:         after > limit,
		})
		summary.TotalBefore += before
		summary.TotalAfter += after
	}
	summary.TotalDelta = summary.TotalAfter - summary.TotalBefore
	return diffs, summary, nil
}

func formatSkillDiffTable(baseRef, headRef string, threshold float64, diffs []skillDiff, summary diffSummary) string {
	if len(diffs) == 0 {
		return "No skill token changes detected.\n"
	}

	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "🔢 Token Budget Diff (%s → %s)\n\n", baseRef, headRef)
	sb.WriteString("| Path | Before | After | Delta | Status |\n")
	sb.WriteString("|-------|--------|-------|-------|--------|\n")

	for _, d := range diffs {
		delta := fmt.Sprintf("%d", d.Delta)
		if d.Delta > 0 {
			delta = fmt.Sprintf("+%d", d.Delta)
		}
		status := "✅ No change"
		switch {
		case d.OverLimit:
			status = fmt.Sprintf("❌ Over limit (%d)", d.Limit)
		case d.Delta < 0:
			status = fmt.Sprintf("✅ %.1f%%", d.PercentChange)
		case d.Delta > 0 && d.ThresholdExceeded:
			status = fmt.Sprintf("⚠️ +%.1f%%", d.PercentChange)
		case d.Delta > 0:
			status = fmt.Sprintf("ℹ️ +%.1f%%", d.PercentChange)
		}
		_, _ = fmt.Fprintf(&sb, "| %s | %d | %d | %s | %s |\n", d.Path, d.Before, d.After, delta, status)
	}

	sb.WriteString("\n")
	_, _ = fmt.Fprintf(&sb, "Threshold: %.1f%%\n", threshold)
	_, _ = fmt.Fprintf(&sb, "Total: %d → %d (%+d)\n", summary.TotalBefore, summary.TotalAfter, summary.TotalDelta)
	return sb.String()
}

func countSkillTokensAtRef(rootDir, ref, relPath string, counter tokens.Counter) int {
	if relPath == "" {
		return 0
	}
	content := ""
	if ref == git.WorkingTreeRef {
		data, err := os.ReadFile(filepath.Join(rootDir, relPath))
		if err != nil {
			return 0
		}
		content = string(data)
	} else {
		c, err := git.GetFileFromRef(rootDir, relPath, ref)
		if err != nil {
			return 0
		}
		content = c
	}
	return counter.Count(content)
}

func collectSkillFilesForRef(rootDir, ref string, roots []string) map[string]string {
	files := listRefFiles(rootDir, ref)
	skills := map[string]string{}
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
			prefix := root + "/"
			if strings.HasPrefix(normalized, prefix) {
				skills[normalized] = normalized
				break
			}
		}
	}
	return skills
}

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

func percentageDiff(before, delta int) float64 {
	if before <= 0 {
		if delta > 0 {
			return 100
		}
		return 0
	}
	return float64(delta) * 100 / float64(before)
}

func loadDiffLimitsConfig(rootDir string) checks.TokenLimitsConfig {
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
