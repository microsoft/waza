package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	cmd.Flags().String("format", "text", "Output format: text | json")
	return cmd
}

// --- JSON output structs ---

type checkJSONReport struct {
	Timestamp string            `json:"timestamp"`
	Skills    []skillJSONReport `json:"skills"`
}

type skillJSONReport struct {
	Name           string          `json:"name"`
	Path           string          `json:"path"`
	Ready          bool            `json:"ready"`
	Compliance     complianceJSON  `json:"compliance"`
	TokenBudget    tokenBudgetJSON `json:"tokenBudget"`
	SpecCompliance []checkItemJSON `json:"specCompliance"`
	McpIntegration *mcpJSON        `json:"mcpIntegration,omitempty"`
	Links          *linksJSON      `json:"links,omitempty"`
	Eval           evalJSON        `json:"eval"`
	Schema         *schemaJSON     `json:"schema,omitempty"`
	AdvisoryChecks []checkItemJSON `json:"advisoryChecks,omitempty"`
	NextSteps      []string        `json:"nextSteps"`
}

type complianceJSON struct {
	Level  string      `json:"level"`
	Issues []issueJSON `json:"issues,omitempty"`
}

type issueJSON struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type tokenBudgetJSON struct {
	Count    int    `json:"count"`
	Limit    int    `json:"limit"`
	Exceeded bool   `json:"exceeded"`
	Warning  bool   `json:"warning"`
	Status   string `json:"status"` // "ok", "warning", "exceeded"
}

type checkItemJSON struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Summary  string `json:"summary"`
	Status   string `json:"status,omitempty"`   // "ok", "optimal", "warning"
	Evidence string `json:"evidence,omitempty"` // supporting detail
}

type mcpJSON struct {
	Score  int         `json:"score"`
	Total  int         `json:"total"`
	Issues []issueJSON `json:"issues,omitempty"`
}

type linkIssueJSON struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type linksJSON struct {
	Valid          int             `json:"valid"`
	Total          int             `json:"total"`
	Passed         bool            `json:"passed"`
	BrokenLinks    []linkIssueJSON `json:"brokenLinks,omitempty"`
	DirectoryLinks []linkIssueJSON `json:"directoryLinks,omitempty"`
	ScopeEscapes   []linkIssueJSON `json:"scopeEscapes,omitempty"`
	DeadURLs       []linkIssueJSON `json:"deadURLs,omitempty"`
	OrphanedFiles  []string        `json:"orphanedFiles,omitempty"`
}

type evalJSON struct {
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
}

type schemaJSON struct {
	EvalErrors []string            `json:"evalErrors,omitempty"`
	TaskErrors map[string][]string `json:"taskErrors,omitempty"`
	Valid      bool                `json:"valid"`
}

type readinessReport struct {
	mcpResult           *dev.McpResult
	linkResult          *dev.LinkResult
	complianceScore     *scoring.ScoreResult
	complianceLevel     scoring.AdherenceLevel
	tokenCount          int
	tokenLimit          int
	tokenExceeded       bool
	tokenWarning        bool
	hasEval             bool
	skillName           string
	skillPath           string
	evalPath            string                // resolved path to eval.yaml (empty if not found)
	evalSchemaErrs      []string              // eval.yaml schema validation errors
	taskSchemaErrs      map[string][]string   // per-task-file schema errors (key = relative path)
	scoreSpecChecks     []*checks.CheckResult // spec compliance checks from score-command
	scoreAdvisoryChecks []*checks.CheckResult // advisory checks from score-command
}

func runCheck(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	if format != "text" && format != "json" {
		return fmt.Errorf("invalid format %q: expected text or json", format)
	}

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
		return outputCheckReport(cmd, format, []*readinessReport{report})
	}

	// Try workspace detection
	wsCtx, err := resolveWorkspace(args)
	if err == nil && len(wsCtx.Skills) > 0 {
		return runCheckForSkills(cmd, wsCtx, format)
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

	return outputCheckReport(cmd, format, []*readinessReport{report})
}

func runCheckForSkills(cmd *cobra.Command, wsCtx *workspace.WorkspaceContext, format string) error {
	w := cmd.OutOrStdout()
	var reports []*readinessReport

	for i, si := range wsCtx.Skills {
		if format == "text" && len(wsCtx.Skills) > 1 {
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
		if format == "text" {
			displayReadinessReport(w, report)
		}
	}

	if format == "text" && len(wsCtx.Skills) > 1 {
		printCheckSummaryTable(w, reports)
	}

	if format == "json" {
		return outputCheckJSON(cmd, reports)
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
		for _, c := range r.scoreSpecChecks {
			if !c.Passed {
				specStatus = "❌"
				break
			}
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

	// 3b. Run MCP integration checks
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

	// 7. Run score-command spec and advisory checks
	var checkErrs []error
	specResults, err := checks.RunChecks(checks.SpecCheckers(), sk)
	if err != nil {
		checkErrs = append(checkErrs, err)
	}
	report.scoreSpecChecks = specResults

	advisoryResults, err := checks.RunChecks(checks.AdvisoryCheckers(), sk)
	if err != nil {
		checkErrs = append(checkErrs, err)
	}
	report.scoreAdvisoryChecks = advisoryResults

	if len(checkErrs) > 0 {
		return report, errors.Join(checkErrs...)
	}

	return report, nil
}

// resolveSkillTokenLimit loads per-skill token limits from .waza.yaml
// (primary) or .token-limits.json (fallback) and returns the resolved
// limit for SKILL.md.
// Falls back to 0 (which lets TokenBudgetChecker use scoring.TokenSoftLimit).
func resolveSkillTokenLimit(startDir string) int {
	// Try project config first (.waza.yaml tokens.limits section)
	if cfg, err := projectconfig.Load(startDir); err == nil && cfg.Tokens.Limits != nil {
		limCfg := checks.TokenLimitsConfig{
			Defaults:  cfg.Tokens.Limits.Defaults,
			Overrides: cfg.Tokens.Limits.Overrides,
		}
		if limCfg.Defaults != nil {
			// Compute workspace-relative prefix so workspace-root-relative
			// patterns (e.g. "plugin/skills/**/SKILL.md") can match.
			prefix := ""
			if wd, wdErr := os.Getwd(); wdErr == nil {
				if abs, absErr := filepath.Abs(startDir); absErr == nil {
					if rel, relErr := filepath.Rel(wd, abs); relErr == nil && rel != "." {
						prefix = filepath.ToSlash(rel)
					}
				}
			}
			lr := checks.GetLimitForFile("SKILL.md", limCfg, prefix)
			return lr.Limit
		}
	}

	// Try .token-limits.json (fallback)
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

// outputCheckReport dispatches to text or JSON output.
func outputCheckReport(cmd *cobra.Command, format string, reports []*readinessReport) error {
	if format == "json" {
		return outputCheckJSON(cmd, reports)
	}
	w := cmd.OutOrStdout()
	for _, report := range reports {
		displayReadinessReport(w, report)
	}
	return nil
}

// outputCheckJSON marshals reports as JSON to the command's stdout.
func outputCheckJSON(cmd *cobra.Command, reports []*readinessReport) error {
	jsonReport := checkJSONReport{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Skills:    make([]skillJSONReport, 0, len(reports)),
	}
	for _, r := range reports {
		jsonReport.Skills = append(jsonReport.Skills, buildSkillJSON(r))
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(jsonReport); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
	return err
}

// buildSkillJSON converts a readinessReport to its JSON representation.
func buildSkillJSON(report *readinessReport) skillJSONReport {
	specChecksPassed := true
	for _, c := range report.scoreSpecChecks {
		if !c.Passed {
			specChecksPassed = false
			break
		}
	}
	isReady := report.complianceLevel.AtLeast(scoring.AdherenceMediumHigh) &&
		!report.tokenExceeded &&
		(report.linkResult == nil || report.linkResult.Passed()) &&
		len(report.evalSchemaErrs) == 0 &&
		len(report.taskSchemaErrs) == 0 &&
		specChecksPassed

	skillName := report.skillName
	if skillName == "" {
		skillName = "unnamed-skill"
	}

	jr := skillJSONReport{
		Name:  skillName,
		Path:  report.skillPath,
		Ready: isReady,
	}

	// Compliance
	jr.Compliance = complianceJSON{Level: string(report.complianceLevel)}
	if report.complianceScore != nil {
		for _, iss := range report.complianceScore.Issues {
			jr.Compliance.Issues = append(jr.Compliance.Issues, issueJSON{
				Severity: iss.Severity,
				Message:  iss.Message,
			})
		}
	}

	// Token budget
	tokenStatus := "ok"
	if report.tokenExceeded {
		tokenStatus = "exceeded"
	} else if report.tokenWarning {
		tokenStatus = "warning"
	}
	jr.TokenBudget = tokenBudgetJSON{
		Count:    report.tokenCount,
		Limit:    report.tokenLimit,
		Exceeded: report.tokenExceeded,
		Warning:  report.tokenWarning,
		Status:   tokenStatus,
	}

	// Spec compliance
	for _, c := range report.scoreSpecChecks {
		item := checkItemJSON{
			Name:    c.Name,
			Passed:  c.Passed,
			Summary: c.Summary,
		}
		if sh, ok := c.Data.(checks.StatusHolder); ok {
			item.Status = string(sh.GetStatus())
		}
		if d, ok := c.Data.(*checks.ScoreCheckData); ok && d.Evidence != "" {
			item.Evidence = d.Evidence
		}
		jr.SpecCompliance = append(jr.SpecCompliance, item)
	}

	// MCP integration
	if report.mcpResult != nil {
		mcp := &mcpJSON{Score: report.mcpResult.SubScore, Total: 4}
		for _, iss := range report.mcpResult.Issues {
			mcp.Issues = append(mcp.Issues, issueJSON{
				Severity: iss.Severity,
				Message:  fmt.Sprintf("[%s] %s", iss.Rule, iss.Message),
			})
		}
		jr.McpIntegration = mcp
	}

	// Links
	if report.linkResult != nil {
		lr := &linksJSON{
			Valid:  report.linkResult.ValidLinks,
			Total:  report.linkResult.TotalLinks,
			Passed: report.linkResult.Passed(),
		}
		for _, bl := range report.linkResult.BrokenLinks {
			lr.BrokenLinks = append(lr.BrokenLinks, linkIssueJSON{Source: bl.Source, Target: bl.Target, Reason: bl.Reason})
		}
		for _, dl := range report.linkResult.DirectoryLinks {
			lr.DirectoryLinks = append(lr.DirectoryLinks, linkIssueJSON{Source: dl.Source, Target: dl.Target, Reason: dl.Reason})
		}
		for _, se := range report.linkResult.ScopeEscapes {
			lr.ScopeEscapes = append(lr.ScopeEscapes, linkIssueJSON{Source: se.Source, Target: se.Target, Reason: se.Reason})
		}
		for _, du := range report.linkResult.DeadURLs {
			lr.DeadURLs = append(lr.DeadURLs, linkIssueJSON{Source: du.Source, Target: du.Target, Reason: du.Reason})
		}
		lr.OrphanedFiles = report.linkResult.OrphanedFiles
		jr.Links = lr
	}

	// Eval
	jr.Eval = evalJSON{Found: report.hasEval, Path: report.evalPath}

	// Schema
	if report.hasEval {
		s := &schemaJSON{
			Valid:      len(report.evalSchemaErrs) == 0 && len(report.taskSchemaErrs) == 0,
			EvalErrors: report.evalSchemaErrs,
			TaskErrors: report.taskSchemaErrs,
		}
		jr.Schema = s
	}

	// Advisory checks
	for _, c := range report.scoreAdvisoryChecks {
		item := checkItemJSON{
			Name:    c.Name,
			Passed:  c.Passed,
			Summary: c.Summary,
		}
		if sh, ok := c.Data.(checks.StatusHolder); ok {
			item.Status = string(sh.GetStatus())
		}
		if d, ok := c.Data.(*checks.ScoreCheckData); ok && d.Evidence != "" {
			item.Evidence = d.Evidence
		}
		jr.AdvisoryChecks = append(jr.AdvisoryChecks, item)
	}

	// Next steps
	jr.NextSteps = generateNextSteps(report)

	return jr
}

// ---------------------------------------------------------------------------
// Shared display helpers — single source of truth for check output formatting.
//
// Convention:
//   Section header:  "emoji Title: summary\n"
//   Status line:     "   emoji  message\n"   (3-space indent, emoji, 2-space gap)
//   Detail line:     "   emoji  [label] message\n"
//   Evidence line:   "     📎  evidence\n"   (5-space indent for sub-detail)
//
// 3-state icons:  ✅ = ok/passed   ⚠️ = warning   ❌ = error/failed
// ---------------------------------------------------------------------------

type writer = interface{ Write([]byte) (int, error) }

// writeSection prints a section header: "emoji Title: summary\n".
//
//nolint:errcheck
func writeSection(w writer, emoji, title, summary string) {
	if summary != "" {
		fmt.Fprintf(w, "%s %s: %s\n", emoji, title, summary)
	} else {
		fmt.Fprintf(w, "%s %s\n", emoji, title)
	}
}

// writeStatus prints a status line: "   icon  message\n".
//
//nolint:errcheck
func writeStatus(w writer, icon, message string) {
	fmt.Fprintf(w, "   %s  %s\n", icon, message)
}

// statusIcon returns the standard 3-state icon for the given state.
func statusIcon(state string) string {
	switch state {
	case "ok":
		return "✅"
	case "warning":
		return "⚠️"
	case "error":
		return "❌"
	default:
		return "—"
	}
}

// writeCheckItems renders a list of CheckResult items using the shared format.
//
//nolint:errcheck
func writeCheckItems(w writer, results []*checks.CheckResult, showPassed bool) {
	for _, r := range results {
		icon := "✅"
		if sh, ok := r.Data.(checks.StatusHolder); ok {
			switch sh.GetStatus() {
			case checks.StatusWarning:
				icon = "⚠️"
			case checks.StatusOptimal:
				icon = "✅"
			}
		}
		if !r.Passed {
			icon = "❌"
		}

		if !showPassed && r.Passed {
			continue
		}

		writeStatus(w, icon, fmt.Sprintf("[%s] %s", r.Name, r.Summary))

		if d, ok := r.Data.(*checks.ScoreCheckData); ok && d.Evidence != "" && (!r.Passed || d.Status == checks.StatusWarning) {
			fmt.Fprintf(w, "     📎  %s\n", d.Evidence)
		}
	}
}

//nolint:errcheck // display function — fmt.Fprintf errors to stdout are not actionable
func displayReadinessReport(out writer, report *readinessReport) {
	w := out

	// Header
	fmt.Fprintf(w, "\n🔍 Skill Readiness Check\n")
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	skillName := report.skillName
	if skillName == "" {
		skillName = "unnamed-skill"
	}
	fmt.Fprintf(w, "Skill: %s\n\n", skillName)

	// 1. Compliance Check
	writeSection(w, "📋", "Compliance Score", string(report.complianceLevel))
	switch report.complianceLevel {
	case scoring.AdherenceHigh:
		writeStatus(w, statusIcon("ok"), "Excellent! Your skill meets all compliance requirements.")
	case scoring.AdherenceMediumHigh:
		writeStatus(w, statusIcon("warning"), "Good, but could be improved. Missing routing clarity.")
	case scoring.AdherenceMedium:
		writeStatus(w, statusIcon("warning"), "Needs improvement. Missing anti-triggers and routing clarity.")
	default:
		writeStatus(w, statusIcon("error"), "Needs significant improvement. Description too short or missing triggers.")
	}
	if len(report.complianceScore.Issues) > 0 {
		fmt.Fprintf(w, "\n   Issues found:\n")
		for _, issue := range report.complianceScore.Issues {
			state := "warning"
			if issue.Severity == "error" {
				state = "error"
			}
			writeStatus(w, statusIcon(state), issue.Message)
		}
	}
	fmt.Fprintf(w, "\n")

	// 2. Spec Compliance
	if len(report.scoreSpecChecks) > 0 {
		pass := 0
		for _, c := range report.scoreSpecChecks {
			if c.Passed {
				pass++
			}
		}
		total := len(report.scoreSpecChecks)
		writeSection(w, "📐", "Spec Compliance", fmt.Sprintf("%d/%d checks passed", pass, total))
		if pass == total {
			writeStatus(w, statusIcon("ok"), "Meets agentskills.io specification.")
		} else {
			writeStatus(w, statusIcon("error"), "Does not fully meet agentskills.io specification.")
		}
		writeCheckItems(w, report.scoreSpecChecks, false)
		fmt.Fprintf(w, "\n")
	}

	// 3. MCP Integration
	if report.mcpResult != nil {
		writeSection(w, "🔌", "MCP Integration", fmt.Sprintf("%d/4", report.mcpResult.SubScore))
		if report.mcpResult.SubScore == 4 {
			writeStatus(w, statusIcon("ok"), "All MCP integration checks passed.")
		} else {
			writeStatus(w, statusIcon("warning"), fmt.Sprintf("MCP documentation incomplete (%d/4 checks passed).", report.mcpResult.SubScore))
		}
		for _, issue := range report.mcpResult.Issues {
			state := "warning"
			if issue.Severity == "error" {
				state = "error"
			}
			writeStatus(w, statusIcon(state), fmt.Sprintf("[%s] %s", issue.Rule, issue.Message))
		}
		fmt.Fprintf(w, "\n")
	}

	// 4. Link Validation
	if report.linkResult != nil {
		writeSection(w, "📎", "Links", fmt.Sprintf("%d/%d valid", report.linkResult.ValidLinks, report.linkResult.TotalLinks))
		if report.linkResult.Passed() {
			if report.linkResult.TotalLinks == 0 {
				writeStatus(w, statusIcon("neutral"), "No links found.")
			} else {
				writeStatus(w, statusIcon("ok"), "All links valid.")
			}
		} else {
			problems := len(report.linkResult.BrokenLinks) + len(report.linkResult.DirectoryLinks) +
				len(report.linkResult.ScopeEscapes) + len(report.linkResult.DeadURLs) +
				len(report.linkResult.OrphanedFiles)
			writeStatus(w, statusIcon("warning"), fmt.Sprintf("%d link issue(s) found.", problems))
		}
		for _, bl := range report.linkResult.BrokenLinks {
			writeStatus(w, statusIcon("error"), fmt.Sprintf("[%s] → %s: %s", bl.Source, bl.Target, bl.Reason))
		}
		for _, dl := range report.linkResult.DirectoryLinks {
			writeStatus(w, statusIcon("warning"), fmt.Sprintf("[%s] → %s: %s", dl.Source, dl.Target, dl.Reason))
		}
		for _, se := range report.linkResult.ScopeEscapes {
			writeStatus(w, statusIcon("error"), fmt.Sprintf("[%s] → %s: %s", se.Source, se.Target, se.Reason))
		}
		for _, du := range report.linkResult.DeadURLs {
			writeStatus(w, statusIcon("warning"), fmt.Sprintf("[%s] → %s: %s", du.Source, du.Target, du.Reason))
		}
		if len(report.linkResult.OrphanedFiles) > 0 {
			fmt.Fprintf(w, "\n   Orphaned files in references/:\n")
			for _, f := range report.linkResult.OrphanedFiles {
				writeStatus(w, statusIcon("warning"), f)
			}
		}
		fmt.Fprintf(w, "\n")
	}

	// 5. Token Budget Check
	writeSection(w, "📊", "Token Budget", fmt.Sprintf("%d / %d tokens", report.tokenCount, report.tokenLimit))
	if report.tokenExceeded {
		over := report.tokenCount - report.tokenLimit
		writeStatus(w, statusIcon("error"), fmt.Sprintf("Exceeds limit by %d tokens. Consider reducing content.", over))
	} else if report.tokenWarning {
		remaining := report.tokenLimit - report.tokenCount
		writeStatus(w, statusIcon("warning"), fmt.Sprintf("Approaching limit (%d tokens remaining). Consider optimizing.", remaining))
	} else {
		remaining := report.tokenLimit - report.tokenCount
		writeStatus(w, statusIcon("ok"), fmt.Sprintf("Within budget (%d tokens remaining).", remaining))
	}
	fmt.Fprintf(w, "\n")

	// 6. Evaluation Check
	if report.hasEval {
		writeSection(w, "🧪", "Evaluation Suite", "Found")
		writeStatus(w, statusIcon("ok"), "eval.yaml detected. Run 'waza run eval.yaml' to test.")
	} else {
		writeSection(w, "🧪", "Evaluation Suite", "Not Found")
		writeStatus(w, statusIcon("warning"), "No eval.yaml found. Consider creating tests.")
	}
	fmt.Fprintf(w, "\n")

	// 7. Schema Validation (only when eval exists)
	if report.hasEval {
		hasEvalSchemaErrs := len(report.evalSchemaErrs) > 0
		hasTaskSchemaErrs := len(report.taskSchemaErrs) > 0

		if hasEvalSchemaErrs {
			writeSection(w, "📐", "Eval Schema", fmt.Sprintf("%d error(s)", len(report.evalSchemaErrs)))
			for _, e := range report.evalSchemaErrs {
				writeStatus(w, statusIcon("error"), e)
			}
			fmt.Fprintf(w, "\n")
		}
		if hasTaskSchemaErrs {
			writeSection(w, "📐", "Task Schema", fmt.Sprintf("%d file(s) with errors", len(report.taskSchemaErrs)))
			for file, errs := range report.taskSchemaErrs {
				fmt.Fprintf(w, "   %s:\n", file)
				for _, e := range errs {
					fmt.Fprintf(w, "     ❌  %s\n", e)
				}
			}
			fmt.Fprintf(w, "\n")
		}
		if !hasEvalSchemaErrs && !hasTaskSchemaErrs {
			writeSection(w, "📐", "Schema Validation", "Passed")
			writeStatus(w, statusIcon("ok"), "eval.yaml schema valid")
			taskCount := countValidatedTasks(report)
			if taskCount > 0 {
				writeStatus(w, statusIcon("ok"), fmt.Sprintf("%d task file(s) validated", taskCount))
			}
			fmt.Fprintf(w, "\n")
		}
	}

	// 8. Advisory Checks
	if len(report.scoreAdvisoryChecks) > 0 {
		writeSection(w, "💡", "Advisory Checks", "")
		writeCheckItems(w, report.scoreAdvisoryChecks, true)
		fmt.Fprintf(w, "\n")
	}

	// Overall Readiness Assessment
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Fprintf(w, "📈 Overall Readiness\n")
	fmt.Fprintf(w, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	specChecksPassed := true
	for _, c := range report.scoreSpecChecks {
		if !c.Passed {
			specChecksPassed = false
			break
		}
	}
	isReady := report.complianceLevel.AtLeast(scoring.AdherenceMediumHigh) &&
		!report.tokenExceeded &&
		(report.linkResult == nil || report.linkResult.Passed()) &&
		len(report.evalSchemaErrs) == 0 &&
		len(report.taskSchemaErrs) == 0 &&
		specChecksPassed

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
	for _, c := range report.scoreSpecChecks {
		if !c.Passed {
			steps = append(steps, fmt.Sprintf("Fix spec violation [%s]: %s", c.Name, c.Summary))
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
