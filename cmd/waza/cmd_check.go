package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spboyer/waza/cmd/waza/dev"
	"github.com/spboyer/waza/internal/skill"
	internalTokens "github.com/spboyer/waza/internal/tokens"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
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
	complianceScore *dev.ScoreResult
	complianceLevel dev.AdherenceLevel
	tokenCount      int
	tokenLimit      int
	tokenExceeded   bool
	hasEval         bool
	skillName       string
	skillPath       string
}

func runCheck(cmd *cobra.Command, args []string) error {
	// Try workspace detection first
	skills, err := resolveSkillsFromArgs(args)
	if err == nil && len(skills) > 0 {
		return runCheckForSkills(cmd, skills)
	}

	// Fallback: explicit path (backward compatible)
	skillDir := "."
	if len(args) > 0 {
		skillDir = args[0]
	}
	if !filepath.IsAbs(skillDir) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		skillDir = filepath.Join(wd, skillDir)
	}

	report, err := checkReadiness(skillDir)
	if err != nil {
		return err
	}

	displayReadinessReport(cmd.OutOrStdout(), report)
	return nil
}

func runCheckForSkills(cmd *cobra.Command, skills []workspace.SkillInfo) error {
	w := cmd.OutOrStdout()
	var reports []*readinessReport

	for i, si := range skills {
		if len(skills) > 1 {
			if i > 0 {
				fmt.Fprintln(w) //nolint:errcheck
			}
			fmt.Fprintf(w, "\n=== %s ===\n", si.Name) //nolint:errcheck
		}

		report, err := checkReadiness(si.Dir)
		if err != nil {
			return fmt.Errorf("checking skill %s: %w", si.Name, err)
		}
		reports = append(reports, report)
		displayReadinessReport(w, report)
	}

	if len(skills) > 1 {
		printCheckSummaryTable(w, reports)
	}
	return nil
}

func printCheckSummaryTable(w interface{ Write([]byte) (int, error) }, reports []*readinessReport) {
	fmt.Fprintf(w, "\n")                                                              //nolint:errcheck
	fmt.Fprintf(w, "═══════════════════════════════════════════════\n")               //nolint:errcheck
	fmt.Fprintf(w, " CHECK SUMMARY\n")                                                //nolint:errcheck
	fmt.Fprintf(w, "═══════════════════════════════════════════════\n\n")             //nolint:errcheck
	fmt.Fprintf(w, "%-25s %-15s %-12s %s\n", "Skill", "Compliance", "Tokens", "Eval") //nolint:errcheck
	fmt.Fprintf(w, "%s\n", strings.Repeat("─", 60))                                   //nolint:errcheck

	for _, r := range reports {
		name := r.skillName
		if name == "" {
			name = "unnamed"
		}
		tokenStatus := "✅"
		if r.tokenExceeded {
			tokenStatus = "❌"
		}
		evalStatus := "✅"
		if !r.hasEval {
			evalStatus = "⚠️"
		}
		fmt.Fprintf(w, "%-25s %-15s %s %d/%-6d %s\n", //nolint:errcheck
			name, r.complianceLevel, tokenStatus, r.tokenCount, r.tokenLimit, evalStatus)
	}
	fmt.Fprintf(w, "\n") //nolint:errcheck
}

func checkReadiness(skillDir string) (*readinessReport, error) {
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
	scorer := &dev.HeuristicScorer{}
	report.complianceScore = scorer.Score(&sk)
	report.complianceLevel = report.complianceScore.Level

	// 4. Check token budget
	counter, err := internalTokens.NewCounter(internalTokens.TokenizerDefault)
	if err != nil {
		return report, err
	}
	tokens := counter.Count(string(data))
	report.tokenCount = tokens

	// Use default token limit for SKILL.md (500 tokens is the standard)
	report.tokenLimit = 500
	report.tokenExceeded = report.tokenCount > report.tokenLimit

	// 5. Check for eval.yaml (try workspace-aware detection first, then co-located)
	wd, wdErr := os.Getwd()
	if wdErr == nil {
		if ctx, ctxErr := workspace.DetectContext(wd); ctxErr == nil && ctx.Type != workspace.ContextNone {
			if evalPath, findErr := workspace.FindEval(ctx, sk.Frontmatter.Name); findErr == nil && evalPath != "" {
				report.hasEval = true
				return report, nil
			}
		}
	}
	evalPath := filepath.Join(skillDir, "eval.yaml")
	if _, err := os.Stat(evalPath); err == nil {
		report.hasEval = true
	}

	return report, nil
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
	case dev.AdherenceHigh:
		fmt.Fprintf(w, "   ✅ Excellent! Your skill meets all compliance requirements.\n")
	case dev.AdherenceMediumHigh:
		fmt.Fprintf(w, "   ⚠️  Good, but could be improved. Missing routing clarity.\n")
	case dev.AdherenceMedium:
		fmt.Fprintf(w, "   ⚠️  Needs improvement. Missing anti-triggers and routing clarity.\n")
	default:
		fmt.Fprintf(w, "   ❌ Needs significant improvement. Description too short or missing triggers.\n")
	}

	if len(report.complianceScore.Issues) > 0 {
		fmt.Fprintf(w, "   Issues found:\n")
		for _, issue := range report.complianceScore.Issues {
			emoji := "⚠️"
			if issue.Severity == "error" {
				emoji = "❌"
			}
			fmt.Fprintf(w, "   %s %s\n", emoji, issue.Message)
		}
	}
	fmt.Fprintf(w, "\n")

	// 2. Token Budget Check
	fmt.Fprintf(w, "📊 Token Budget: %d / %d tokens\n", report.tokenCount, report.tokenLimit)
	if report.tokenExceeded {
		over := report.tokenCount - report.tokenLimit
		fmt.Fprintf(w, "   ❌ Exceeds limit by %d tokens. Consider reducing content.\n", over)
	} else {
		remaining := report.tokenLimit - report.tokenCount
		fmt.Fprintf(w, "   ✅ Within budget (%d tokens remaining).\n", remaining)
	}
	fmt.Fprintf(w, "\n")

	// 3. Evaluation Check
	fmt.Fprintf(w, "🧪 Evaluation Suite: ")
	if report.hasEval {
		fmt.Fprintf(w, "Found\n")
		fmt.Fprintf(w, "   ✅ eval.yaml detected. Run 'waza run eval.yaml' to test.\n")
	} else {
		fmt.Fprintf(w, "Not Found\n")
		fmt.Fprintf(w, "   ⚠️  No eval.yaml found. Consider creating tests.\n")
	}
	fmt.Fprintf(w, "\n")

	// Overall Readiness Assessment
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(w, "📈 Overall Readiness\n")
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	isReady := report.complianceLevel.AtLeast(dev.AdherenceMediumHigh) &&
		!report.tokenExceeded

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
		fmt.Fprintf(w, "  • Submitting a PR to microsoft/skills\n")
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
	if !report.complianceLevel.AtLeast(dev.AdherenceHigh) {
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

	// Token budget issues (second priority)
	if report.tokenExceeded {
		over := report.tokenCount - report.tokenLimit
		steps = append(steps, fmt.Sprintf("Reduce SKILL.md by %d tokens. Run 'waza tokens suggest' for optimization tips", over))
	}

	// Evaluation suite (third priority)
	if !report.hasEval {
		steps = append(steps, "Create an evaluation suite with 'waza init' or 'waza generate SKILL.md'")
	}

	return steps
}
