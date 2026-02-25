package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/spboyer/waza/cmd/waza/dev"
	"github.com/spboyer/waza/internal/checks"
	"github.com/spboyer/waza/internal/projectconfig"
	"github.com/spboyer/waza/internal/scoring"
	"github.com/spboyer/waza/internal/skill"
	"github.com/spboyer/waza/internal/validation"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newCheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check [skill-name | skill-path]",
		Short: "Check if a skill is ready for submission",
		Long: `Check if a skill is ready for submission by running compliance, token, and eval checks.

Performs the following checks:
  1. Compliance scoring - Validates frontmatter adherence (Low/Medium/Medium-High/High)
  2. Token budget - Checks if SKILL.md is within token limits
  3. Evaluation - Checks for eval.yaml presence

Provides a plain-language summary and suggests next steps.

With no arguments, uses workspace detection to find skills automatically:
  - Single-skill workspace → checks that skill
  - Multi-skill workspace → checks ALL skills with summary table

You can also specify a skill name or path:
  waza check code-explainer   # by skill name
  waza check skills/my-skill  # by path
  waza check .                # current directory`,
		Args:          cobra.MaximumNArgs(1),
		RunE:          runCheck,
		SilenceErrors: true,
	}
	return cmd
}

type readinessReport struct {
	specResult      *dev.SpecResult
	mcpResult       *dev.McpResult
	linkResult      *dev.LinkResult
	complianceScore *scoring.ScoreResult
	complianceLevel scoring.AdherenceLevel
	tokenCount      int
	tokenLimit      int
	tokenExceeded   bool
	tokenWarning    bool
	hasEval         bool
	skillName       string
	skillPath       string
	evalPath        string              // resolved path to eval.yaml (empty if not found)
	evalSchemaErrs  []string            // eval.yaml schema validation errors
	taskSchemaErrs  map[string][]string // per-task-file schema errors (key = relative path)
}

func runCheck(cmd *cobra.Command, args []string) error {
	// If arg looks like a file path, use it directly
	if len(args) > 0 && workspace.LooksLikePath(args[0]) {
		skillDir := args[0]
		if !filepath.IsAbs(skillDir) {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			skillDir = filepath.Join(wd, skillDir)
		}
		report, err := checkReadiness(skillDir, nil)
		if err != nil {
			return err
		}
		displayReadinessReport(cmd.OutOrStdout(), report)
		return nil
	}

	// Try workspace detection
	wsCtx, err := resolveWorkspace(args)
	if err == nil && len(wsCtx.Skills) > 0 {
		return runCheckForSkills(cmd, wsCtx)
	}

	// Fallback: current directory
	skillDir := "."
	if !filepath.IsAbs(skillDir) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		skillDir = filepath.Join(wd, skillDir)
	}

	report, err := checkReadiness(skillDir, nil)
	if err != nil {
		return err
	}

	displayReadinessReport(cmd.OutOrStdout(), report)
	return nil
}

func runCheckForSkills(cmd *cobra.Command, wsCtx *workspace.WorkspaceContext) error {
	w := cmd.OutOrStdout()
	var reports []*readinessReport

	for i, si := range wsCtx.Skills {
		if len(wsCtx.Skills) > 1 {
			if i > 0 {
				fmt.Fprintln(w) //nolint:errcheck
			}
			fmt.Fprintf(w, "\n=== %s ===\n", si.Name) //nolint:errcheck
		}

		report, err := checkReadiness(si.Dir, wsCtx)
		if err != nil {
			return fmt.Errorf("checking skill %s: %w", si.Name, err)
		}
		reports = append(reports, report)
		displayReadinessReport(w, report)
	}

	if len(wsCtx.Skills) > 1 {
		printCheckSummaryTable(w, reports)
	}
	return nil
}

func printCheckSummaryTable(w interface{ Write([]byte) (int, error) }, reports []*readinessReport) {
	const maxNameWidth = 25
	const minNameWidth = 10

	// Compute dynamic column width from the longest skill name.
	nameWidth := len("Skill")
	for _, r := range reports {
		n := r.skillName
		if n == "" {
			n = "unnamed"
		}
		if runeLen := utf8.RuneCountInString(n); runeLen > nameWidth {
			nameWidth = runeLen
		}
	}
	if nameWidth > maxNameWidth {
		nameWidth = maxNameWidth
	}
	if nameWidth < minNameWidth {
		nameWidth = minNameWidth
	}

	// Fixed column widths (display columns) for emoji-safe alignment.
	const colCompliance = 14
	const colTokens = 16
	const colSpec = 6
	const colLinks = 6
	const colSchema = 6
	const colEval = 4
	totalWidth := nameWidth + colCompliance + colTokens + colSpec + colLinks + colSchema + colEval + 12 // 12 = 6 gaps × 2 spaces

	fmt.Fprintf(w, "\n")                                      //nolint:errcheck
	fmt.Fprintf(w, "%s\n", strings.Repeat("═", totalWidth))   //nolint:errcheck
	fmt.Fprintf(w, " CHECK SUMMARY\n")                        //nolint:errcheck
	fmt.Fprintf(w, "%s\n\n", strings.Repeat("═", totalWidth)) //nolint:errcheck

	fmt.Fprintf(w, "%s  %s  %s  %s  %s  %s  %s\n", //nolint:errcheck
		padRight("Skill", nameWidth),
		padRight("Compliance", colCompliance),
		padRight("Tokens", colTokens),
		padRight("Spec", colSpec),
		padRight("Links", colLinks),
		padRight("Schema", colSchema),
		"Eval")
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", totalWidth)) //nolint:errcheck

	for _, r := range reports {
		name := r.skillName
		if name == "" {
			name = "unnamed"
		}
		name = truncateName(name, nameWidth)

		tokenStatus := "✅"
		if r.tokenExceeded {
			tokenStatus = "❌"
		} else if r.tokenWarning {
			tokenStatus = "⚠️ "
		}
		specStatus := "✅"
		if r.specResult != nil && !r.specResult.Passed() {
			specStatus = "❌"
		}
		schemaStatus := "✅"
		if len(r.evalSchemaErrs) > 0 || len(r.taskSchemaErrs) > 0 {
			schemaStatus = "❌"
		} else if !r.hasEval {
			schemaStatus = "—"
		}
		linkStatus := "✅"
		if r.linkResult != nil && !r.linkResult.Passed() {
			linkStatus = "⚠️"
		} else if r.linkResult == nil {
			linkStatus = "—"
		}
		evalStatus := "✅"
		if !r.hasEval {
			evalStatus = "⚠️"
		}
		tokenStr := fmt.Sprintf("%s %d/%d", tokenStatus, r.tokenCount, r.tokenLimit)
		fmt.Fprintf(w, "%s  %s  %s  %s  %s  %s  %s\n", //nolint:errcheck
			padRight(name, nameWidth),
			padRight(string(r.complianceLevel), colCompliance),
			padRight(tokenStr, colTokens),
			padRight(specStatus, colSpec),
			padRight(linkStatus, colLinks),
			padRight(schemaStatus, colSchema),
			evalStatus)
	}
	fmt.Fprintf(w, "\n") //nolint:errcheck
}

// truncateName shortens a name to maxLen runes, replacing the last rune with "…" if needed.
func truncateName(name string, maxLen int) string {
	runes := []rune(name)
	if len(runes) <= maxLen {
		return name
	}
	return string(runes[:maxLen-1]) + "…"
}

// padRight pads s with spaces so its terminal display width reaches width.
func padRight(s string, width int) string {
	sw := runewidth.StringWidth(s)
	if sw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-sw)
}

func checkReadiness(skillDir string, wsCtx *workspace.WorkspaceContext) (*readinessReport, error) {
	report := &readinessReport{}

	// 1. Check for SKILL.md
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillPath); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no SKILL.md found in %s", skillDir)
	} else if err != nil {
		return nil, fmt.Errorf("checking SKILL.md: %w", err)
	}
	report.skillPath = skillPath

	// 2. Load and parse SKILL.md
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}
	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return nil, fmt.Errorf("parsing SKILL.md: %w", err)
	}
	sk.Path = skillPath
	report.skillName = sk.Frontmatter.Name

	// 3. Run compliance scoring
	tokenLimit := resolveSkillTokenLimit(filepath.Dir(skillDir))
	warnThreshold := resolveWarningThreshold(filepath.Dir(skillDir))
	complianceData, err := (&checks.ComplianceScoreChecker{TokenLimit: tokenLimit, WarningThreshold: warnThreshold}).Score(sk)
	if err != nil {
		return nil, err
	}
	report.complianceScore = complianceData.Score
	report.complianceLevel = complianceData.Level

	// 3b. Run spec compliance checks
	specScorer := &dev.SpecScorer{}
	report.specResult = specScorer.Score(&sk)

	// 3c. Run MCP integration checks
	mcpScorer := &dev.McpScorer{}
	report.mcpResult = mcpScorer.Score(&sk)

	// 3d. Run link validation
	linkScorer := &dev.LinkScorer{}
	report.linkResult = linkScorer.Score(&sk)

	// 4. Check token budget (resolve per-skill limit from project config)
	tokenData, err := (&checks.TokenBudgetChecker{Limit: tokenLimit}).Budget(sk)
	if err != nil {
		return nil, err
	}
	report.tokenCount = tokenData.TokenCount
	report.tokenLimit = tokenData.TokenLimit
	report.tokenExceeded = tokenData.Exceeded

	// 4b. Check token warning threshold
	if !report.tokenExceeded {
		warnThreshold := resolveWarningThreshold(filepath.Dir(skillDir))
		if warnThreshold > 0 && report.tokenCount >= warnThreshold {
			report.tokenWarning = true
		}
	}

	// 5. Check for eval.yaml (try workspace-aware detection first, then co-located)
	if wsCtx != nil {
		if evalPath, findErr := workspace.FindEval(wsCtx, sk.Frontmatter.Name); findErr == nil && evalPath != "" {
			report.hasEval = true
			report.evalPath = evalPath
		}
	}
	if !report.hasEval {
		// Try workspace detection from the working directory
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if autoCtx, ctxErr := workspace.DetectContext(wd); ctxErr == nil {
				if evalPath, findErr := workspace.FindEval(autoCtx, sk.Frontmatter.Name); findErr == nil && evalPath != "" {
					report.hasEval = true
					report.evalPath = evalPath
				}
			}
		}
	}
	if !report.hasEval {
		colocated := filepath.Join(skillDir, "eval.yaml")
		if _, err := os.Stat(colocated); err == nil {
			report.hasEval = true
			report.evalPath = colocated
		}
	}

	// 6. Validate eval.yaml and task schemas
	if report.hasEval && report.evalPath != "" {
		evalErrs, taskErrs, valErr := validation.ValidateEvalFile(report.evalPath)
		if valErr == nil {
			report.evalSchemaErrs = evalErrs
			report.taskSchemaErrs = taskErrs
		} else {
			// Surface validation errors (e.g., unreadable/invalid eval.yaml) via the report
			report.evalSchemaErrs = append(report.evalSchemaErrs, valErr.Error())
		}
	}

	return report, nil
}

// resolveSkillTokenLimit loads per-skill token limits from .waza.yaml (or
// .token-limits.json) and returns the resolved limit for SKILL.md.
// Falls back to 0 (which lets TokenBudgetChecker use scoring.TokenSoftLimit).
func resolveSkillTokenLimit(startDir string) int {
	// Try project config first (.waza.yaml tokens.limits section)
	if cfg, err := projectconfig.Load(startDir); err == nil && cfg.Tokens.Limits != nil {
		limCfg := checks.TokenLimitsConfig{
			Defaults:  cfg.Tokens.Limits.Defaults,
			Overrides: cfg.Tokens.Limits.Overrides,
		}
		if limCfg.Defaults != nil {
			lr := checks.GetLimitForFile("SKILL.md", limCfg)
			return lr.Limit
		}
	}

	// Try .token-limits.json
	limCfg, err := checks.LoadLimitsConfig(startDir)
	if err == nil {
		lr := checks.GetLimitForFile("SKILL.md", limCfg)
		return lr.Limit
	}

	return 0 // fall back to TokenBudgetChecker default (scoring.TokenSoftLimit)
}

// resolveWarningThreshold returns the configured token warning threshold
// from .waza.yaml. Returns 0 if not configured.
func resolveWarningThreshold(startDir string) int {
	cfg, err := projectconfig.Load(startDir)
	if err != nil {
		return 0
	}
	return cfg.Tokens.WarningThreshold
}

//nolint:errcheck // display function — fmt.Fprintf errors to stdout are not actionable
func displayReadinessReport(out interface{ Write([]byte) (int, error) }, report *readinessReport) {
	w := out

	// Header
	fmt.Fprintf(w, "\n🔍 Skill Readiness Check\n")                        //nolint:errcheck
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n") //nolint:errcheck

	skillName := report.skillName
	if skillName == "" {
		skillName = "unnamed-skill"
	}
	fmt.Fprintf(w, "Skill: %s\n\n", skillName) //nolint:errcheck

	// 1. Compliance Check
	fmt.Fprintf(w, "📋 Compliance Score: %s\n", report.complianceLevel)
	switch report.complianceLevel {
	case scoring.AdherenceHigh:
		fmt.Fprintf(w, "   ✅  Excellent! Your skill meets all compliance requirements.\n")
	case scoring.AdherenceMediumHigh:
		fmt.Fprintf(w, "   ⚠️  Good, but could be improved. Missing routing clarity.\n")
	case scoring.AdherenceMedium:
		fmt.Fprintf(w, "   ⚠️  Needs improvement. Missing anti-triggers and routing clarity.\n")
	default:
		fmt.Fprintf(w, "   ❌  Needs significant improvement. Description too short or missing triggers.\n")
	}

	if len(report.complianceScore.Issues) > 0 {
		fmt.Fprintf(w, "   Issues found:\n")
		for _, issue := range report.complianceScore.Issues {
			emoji := "⚠️"
			if issue.Severity == "error" {
				emoji = "❌"
			}
			fmt.Fprintf(w, "   %s  %s\n", emoji, issue.Message)
		}
	}
	fmt.Fprintf(w, "\n")

	// 1b. Spec Compliance
	if report.specResult != nil {
		fmt.Fprintf(w, "📐 Spec Compliance: %d/%d checks passed\n", report.specResult.Pass, report.specResult.Total)
		if report.specResult.Passed() {
			fmt.Fprintf(w, "   ✅  Meets agentskills.io specification.\n")
		} else {
			fmt.Fprintf(w, "   ❌  Does not fully meet agentskills.io specification.\n")
		}
		if len(report.specResult.Issues) > 0 {
			for _, issue := range report.specResult.Issues {
				emoji := "⚠️"
				if issue.Severity == "error" {
					emoji = "❌"
				}
				fmt.Fprintf(w, "   %s  [%s] %s\n", emoji, issue.Rule, issue.Message)
			}
		}
		fmt.Fprintf(w, "\n")
	}

	// 1c. MCP Integration
	if report.mcpResult != nil {
		fmt.Fprintf(w, "🔌 MCP Integration: %d/4\n", report.mcpResult.SubScore)
		if report.mcpResult.SubScore == 4 {
			fmt.Fprintf(w, "   ✅  All MCP integration checks passed.\n")
		} else {
			fmt.Fprintf(w, "   ⚠️  MCP documentation incomplete (%d/4 checks passed).\n", report.mcpResult.SubScore)
		}
		for _, issue := range report.mcpResult.Issues {
			emoji := "⚠️"
			if issue.Severity == "error" {
				emoji = "❌"
			}
			fmt.Fprintf(w, "   %s  [%s] %s\n", emoji, issue.Rule, issue.Message)
		}
		fmt.Fprintf(w, "\n")
	}

	// 1d. Link Validation
	if report.linkResult != nil {
		fmt.Fprintf(w, "📎 Links: %d/%d valid\n", report.linkResult.ValidLinks, report.linkResult.TotalLinks)
		if report.linkResult.Passed() {
			if report.linkResult.TotalLinks == 0 {
				fmt.Fprintf(w, "   — No links found.\n")
			} else {
				fmt.Fprintf(w, "   ✅  All links valid.\n")
			}
		} else {
			problems := len(report.linkResult.BrokenLinks) + len(report.linkResult.DirectoryLinks) +
				len(report.linkResult.ScopeEscapes) + len(report.linkResult.DeadURLs) +
				len(report.linkResult.OrphanedFiles)
			fmt.Fprintf(w, "   ⚠️  %d link issue(s) found.\n", problems)
		}
		for _, bl := range report.linkResult.BrokenLinks {
			fmt.Fprintf(w, "   ❌  [%s] → %s: %s\n", bl.Source, bl.Target, bl.Reason)
		}
		for _, dl := range report.linkResult.DirectoryLinks {
			fmt.Fprintf(w, "   ⚠️  [%s] → %s: %s\n", dl.Source, dl.Target, dl.Reason)
		}
		for _, se := range report.linkResult.ScopeEscapes {
			fmt.Fprintf(w, "   ❌  [%s] → %s: %s\n", se.Source, se.Target, se.Reason)
		}
		for _, du := range report.linkResult.DeadURLs {
			fmt.Fprintf(w, "   ⚠️  [%s] → %s: %s\n", du.Source, du.Target, du.Reason)
		}
		if len(report.linkResult.OrphanedFiles) > 0 {
			fmt.Fprintf(w, "   Orphaned files in references/:\n")
			for _, f := range report.linkResult.OrphanedFiles {
				fmt.Fprintf(w, "   ⚠️  %s\n", f)
			}
		}
		fmt.Fprintf(w, "\n")
	}

	// 2. Token Budget Check
	fmt.Fprintf(w, "📊 Token Budget: %d / %d tokens\n", report.tokenCount, report.tokenLimit)
	if report.tokenExceeded {
		over := report.tokenCount - report.tokenLimit
		fmt.Fprintf(w, "   ❌  Exceeds limit by %d tokens. Consider reducing content.\n", over)
	} else if report.tokenWarning {
		remaining := report.tokenLimit - report.tokenCount
		fmt.Fprintf(w, "   ⚠️  Approaching limit (%d tokens remaining). Consider optimizing.\n", remaining)
	} else {
		remaining := report.tokenLimit - report.tokenCount
		fmt.Fprintf(w, "   ✅  Within budget (%d tokens remaining).\n", remaining)
	}
	fmt.Fprintf(w, "\n")

	// 3. Evaluation Check
	fmt.Fprintf(w, "🧪 Evaluation Suite: ")
	if report.hasEval {
		fmt.Fprintf(w, "Found\n")
		fmt.Fprintf(w, "   ✅  eval.yaml detected. Run 'waza run eval.yaml' to test.\n")
	} else {
		fmt.Fprintf(w, "Not Found\n")
		fmt.Fprintf(w, "   ⚠️  No eval.yaml found. Consider creating tests.\n")
	}
	fmt.Fprintf(w, "\n")

	// 4. Schema Validation (only when eval exists)
	if report.hasEval {
		hasEvalSchemaErrs := len(report.evalSchemaErrs) > 0
		hasTaskSchemaErrs := len(report.taskSchemaErrs) > 0

		if hasEvalSchemaErrs {
			fmt.Fprintf(w, "📐 Eval Schema: %d error(s)\n", len(report.evalSchemaErrs))
			for _, e := range report.evalSchemaErrs {
				fmt.Fprintf(w, "   ❌  %s\n", e)
			}
			fmt.Fprintf(w, "\n")
		}
		if hasTaskSchemaErrs {
			fmt.Fprintf(w, "📐 Task Schema: %d file(s) with errors\n", len(report.taskSchemaErrs))
			for file, errs := range report.taskSchemaErrs {
				fmt.Fprintf(w, "   %s:\n", file)
				for _, e := range errs {
					fmt.Fprintf(w, "     ❌  %s\n", e)
				}
			}
			fmt.Fprintf(w, "\n")
		}
		if !hasEvalSchemaErrs && !hasTaskSchemaErrs {
			fmt.Fprintf(w, "📐 Schema Validation: Passed\n")
			fmt.Fprintf(w, "   ✅  eval.yaml schema valid\n")
			// Count validated task files
			taskCount := countValidatedTasks(report)
			if taskCount > 0 {
				fmt.Fprintf(w, "   ✅  %d task file(s) validated\n", taskCount)
			}
			fmt.Fprintf(w, "\n")
		}
	}

	// Overall Readiness Assessment
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(w, "📈 Overall Readiness\n")
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	isReady := report.complianceLevel.AtLeast(scoring.AdherenceMediumHigh) &&
		!report.tokenExceeded &&
		(report.specResult == nil || report.specResult.Passed()) &&
		(report.linkResult == nil || report.linkResult.Passed()) &&
		len(report.evalSchemaErrs) == 0 &&
		len(report.taskSchemaErrs) == 0

	if isReady {
		fmt.Fprintf(w, "✅ Your skill is ready for submission!\n\n")
	} else {
		fmt.Fprintf(w, "⚠️  Your skill needs some work before submission.\n\n")
	}

	// Next Steps
	fmt.Fprintf(w, "🎯 Next Steps\n")
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	steps := generateNextSteps(report)
	if len(steps) == 0 {
		fmt.Fprintf(w, "✨ No action needed! Your skill looks great.\n")
		fmt.Fprintf(w, "\nConsider:\n")
		fmt.Fprintf(w, "  • Running 'waza run eval.yaml' to verify functionality\n")
		fmt.Fprintf(w, "  • Sharing your skill with the community\n")
	} else {
		fmt.Fprintf(w, "To improve your skill:\n\n")
		for i, step := range steps {
			fmt.Fprintf(w, "%d. %s\n", i+1, step)
		}
	}
	fmt.Fprintf(w, "\n")
}

func generateNextSteps(report *readinessReport) []string {
	var steps []string

	// Compliance issues (highest priority)
	// Use AtLeast method for proper comparison
	if !report.complianceLevel.AtLeast(scoring.AdherenceHigh) {
		if report.complianceScore.DescriptionLen < 150 {
			steps = append(steps, "Expand your description to at least 150 characters with clear usage guidelines")
		}
		if !report.complianceScore.HasTriggers {
			steps = append(steps, "Add a 'USE FOR:' section with 3-5 trigger phrases that activate the skill")
		}
		if !report.complianceScore.HasAntiTriggers {
			steps = append(steps, "Add a 'DO NOT USE FOR:' section to clarify when NOT to use this skill")
		}
		if !report.complianceScore.HasRoutingClarity {
			steps = append(steps, "Add routing clarity (e.g., **UTILITY SKILL**, INVOKES:, FOR SINGLE OPERATIONS:)")
		}
		if len(steps) > 0 {
			steps = append(steps, "Run 'waza dev' for interactive compliance improvement")
		}
	}

	// Spec compliance issues (between compliance and token checks)
	if report.specResult != nil && !report.specResult.Passed() {
		for _, iss := range report.specResult.Issues {
			if iss.Severity == "error" {
				steps = append(steps, fmt.Sprintf("Fix spec violation [%s]: %s", iss.Rule, iss.Message))
			}
		}
	}

	// Link validation issues
	if report.linkResult != nil && !report.linkResult.Passed() {
		if len(report.linkResult.BrokenLinks) > 0 {
			steps = append(steps, fmt.Sprintf("Fix %d broken link(s) — targets do not exist", len(report.linkResult.BrokenLinks)))
		}
		if len(report.linkResult.DirectoryLinks) > 0 {
			steps = append(steps, fmt.Sprintf("Fix %d link(s) pointing to directories instead of files", len(report.linkResult.DirectoryLinks)))
		}
		if len(report.linkResult.ScopeEscapes) > 0 {
			steps = append(steps, fmt.Sprintf("Fix %d link(s) that escape the skill directory", len(report.linkResult.ScopeEscapes)))
		}
		if len(report.linkResult.DeadURLs) > 0 {
			steps = append(steps, fmt.Sprintf("Fix %d dead external URL(s)", len(report.linkResult.DeadURLs)))
		}
		if len(report.linkResult.OrphanedFiles) > 0 {
			steps = append(steps, fmt.Sprintf("Link or remove %d orphaned file(s) in references/", len(report.linkResult.OrphanedFiles)))
		}
	}

	// Token budget issues (second priority)
	if report.tokenExceeded {
		over := report.tokenCount - report.tokenLimit
		steps = append(steps, fmt.Sprintf("Reduce SKILL.md by %d tokens. Run 'waza tokens suggest' for optimization tips", over))
	} else if report.tokenWarning {
		remaining := report.tokenLimit - report.tokenCount
		steps = append(steps, fmt.Sprintf("SKILL.md is approaching the token limit (%d remaining). Run 'waza tokens suggest' for optimization tips", remaining))
	}

	// Evaluation suite (third priority)
	if !report.hasEval {
		steps = append(steps, "Create an evaluation suite with 'waza init' or 'waza generate SKILL.md'")
	}

	// Schema errors (after eval check)
	if len(report.evalSchemaErrs) > 0 {
		steps = append(steps, fmt.Sprintf("Fix %d schema error(s) in eval.yaml", len(report.evalSchemaErrs)))
	}
	if len(report.taskSchemaErrs) > 0 {
		totalErrs := 0
		for _, errs := range report.taskSchemaErrs {
			totalErrs += len(errs)
		}
		steps = append(steps, fmt.Sprintf("Fix %d schema error(s) across %d task file(s)", totalErrs, len(report.taskSchemaErrs)))
	}

	return steps
}

// countValidatedTasks counts task files that were validated (resolved from eval.yaml).
// It re-resolves the task globs from the eval file to determine count.
func countValidatedTasks(report *readinessReport) int {
	if report.evalPath == "" {
		return 0
	}
	data, err := os.ReadFile(report.evalPath)
	if err != nil {
		return 0
	}
	var spec struct {
		Tasks []string `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return 0
	}
	baseDir := filepath.Dir(report.evalPath)
	count := 0
	for _, pattern := range spec.Tasks {
		matches, err := filepath.Glob(filepath.Join(baseDir, pattern))
		if err == nil {
			count += len(matches)
		}
	}
	return count
}
