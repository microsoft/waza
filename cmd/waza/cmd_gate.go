package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
)

// Gate-specific exit codes. These are stable and documented; CI pipelines
// can rely on them to distinguish gate outcomes. They are scoped to the
// `waza gate` command and intentionally do NOT alias the global
// run/compare exit codes (which are 0/1/2 for success/test-failure/config
// error). When `waza gate` exits, the value below overrides the default
// CLI error mapping in main.go via gateExitError.
const (
	GateExitPass    = 0 // All checks passed
	GateExitRegress = 1 // Aggregate regression exceeded threshold, or task-set delta policy fired
	GateExitGolden  = 2 // One or more golden tasks failed in the current results
	GateExitConfErr = 3 // Gate configuration error (bad flags, unreadable input, etc.)
)

// gatePolicy describes how the gate should treat new or removed tasks
// between baseline and current results.
type gatePolicy string

const (
	gatePolicyAllow gatePolicy = "allow"
	gatePolicyWarn  gatePolicy = "warn"
	gatePolicyFail  gatePolicy = "fail"
)

// gateFormat enumerates supported output formats.
type gateFormat string

const (
	gateFormatHuman         gateFormat = "human"
	gateFormatJSON          gateFormat = "json"
	gateFormatMarkdown      gateFormat = "markdown"
	gateFormatGithubActions gateFormat = "github-actions"
)

// gateExitError carries a gate-specific exit code through the standard
// cobra/main error path so we can return rich messages without losing the
// nuanced status code CI expects.
type gateExitError struct {
	code int
	msg  string
}

func (e *gateExitError) Error() string { return e.msg }

// gateOpts holds parsed gate command flags.
type gateOpts struct {
	baselinePath     string
	currentPath      string
	maxRegressionPct float64
	goldenMustPass   bool
	onNewTasks       gatePolicy
	onRemovedTasks   gatePolicy
	format           gateFormat
	// summaryFile, when set, receives the markdown summary in addition to
	// stdout. GitHub Actions sets GITHUB_STEP_SUMMARY automatically.
	summaryFile string
}

// gateTaskRow is one row in the gate report — a single task's baseline vs
// current state.
type gateTaskRow struct {
	TestID         string  `json:"test_id"`
	DisplayName    string  `json:"display_name"`
	Golden         bool    `json:"golden,omitempty"`
	InBaseline     bool    `json:"in_baseline"`
	InCurrent      bool    `json:"in_current"`
	BaselineStatus string  `json:"baseline_status,omitempty"`
	CurrentStatus  string  `json:"current_status,omitempty"`
	BaselinePass   float64 `json:"baseline_pass_rate"`
	CurrentPass    float64 `json:"current_pass_rate"`
	PassRateDelta  float64 `json:"pass_rate_delta"`
	BaselineScore  float64 `json:"baseline_score"`
	CurrentScore   float64 `json:"current_score"`
	ScoreDelta     float64 `json:"score_delta"`
	// Classification: "passed", "regressed", "improved", "golden-fail",
	// "new", "removed".
	Classification string `json:"classification"`
	// Note is a free-form short reason populated when a row is flagged.
	Note string `json:"note,omitempty"`
}

// gateReport is the full machine-readable summary of a gate run.
type gateReport struct {
	BaselineFile     string        `json:"baseline_file"`
	CurrentFile      string        `json:"current_file"`
	BaselineModel    string        `json:"baseline_model,omitempty"`
	CurrentModel     string        `json:"current_model,omitempty"`
	BaselinePassRate float64       `json:"baseline_pass_rate"`
	CurrentPassRate  float64       `json:"current_pass_rate"`
	PassRateDelta    float64       `json:"pass_rate_delta_pct"`
	MaxRegressionPct float64       `json:"max_regression_pct"`
	GoldenMustPass   bool          `json:"golden_must_pass"`
	OnNewTasks       string        `json:"on_new_tasks"`
	OnRemovedTasks   string        `json:"on_removed_tasks"`
	NewTasks         []string      `json:"new_tasks,omitempty"`
	RemovedTasks     []string      `json:"removed_tasks,omitempty"`
	GoldenFailures   []string      `json:"golden_failures,omitempty"`
	Regressions      []string      `json:"regressions,omitempty"`
	Improvements     []string      `json:"improvements,omitempty"`
	Warnings         []string      `json:"warnings,omitempty"`
	Tasks            []gateTaskRow `json:"tasks"`
	// Overall outcome: "pass", "regression", "golden-fail".
	Outcome  string `json:"outcome"`
	ExitCode int    `json:"exit_code"`
}

func newGateCommand() *cobra.Command {
	opts := &gateOpts{
		maxRegressionPct: 0,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyWarn,
		format:           gateFormatHuman,
	}

	var (
		onNew     string
		onRemoved string
		format    string
	)

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Gate a CI run by comparing current results to a baseline",
		Long: `Gate compares two waza result JSON files (a stable baseline and the
current run) and decides whether CI should pass or fail based on:

- aggregate pass-rate regression threshold (--max-regression-pct)
- per-task hard failures on tasks marked golden: true (--golden-must-pass)
- the appearance or disappearance of tasks between baseline and current
  (--on-new-tasks, --on-removed-tasks)

Exit codes are stable and machine-readable:

  0  pass — no regressions
  1  regression — aggregate pass-rate dropped beyond the threshold, or a
     task-set delta policy was set to "fail"
  2  golden-failure — a task with golden: true failed in the current run
  3  config-error — bad flags, missing or unreadable input files

The --format flag selects the output renderer: human, json, markdown, or
github-actions (which also emits ::error::/::warning:: annotations and a
GITHUB_STEP_SUMMARY entry when $GITHUB_STEP_SUMMARY is set).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Re-parse enums into typed values so we get a clear error before doing IO.
			p, err := parseGatePolicy(onNew, "--on-new-tasks")
			if err != nil {
				return &gateExitError{code: GateExitConfErr, msg: err.Error()}
			}
			opts.onNewTasks = p
			p, err = parseGatePolicy(onRemoved, "--on-removed-tasks")
			if err != nil {
				return &gateExitError{code: GateExitConfErr, msg: err.Error()}
			}
			opts.onRemovedTasks = p
			f, err := parseGateFormat(format)
			if err != nil {
				return &gateExitError{code: GateExitConfErr, msg: err.Error()}
			}
			opts.format = f

			if opts.maxRegressionPct < 0 || opts.maxRegressionPct > 100 {
				return &gateExitError{code: GateExitConfErr, msg: fmt.Sprintf("--max-regression-pct must be in [0,100], got %g", opts.maxRegressionPct)}
			}
			if opts.baselinePath == "" || opts.currentPath == "" {
				return &gateExitError{code: GateExitConfErr, msg: "--baseline and --current are required"}
			}

			return runGate(cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.baselinePath, "baseline", "", "Path to baseline results.json (required)")
	cmd.Flags().StringVar(&opts.currentPath, "current", "", "Path to current results.json (required)")
	cmd.Flags().Float64Var(&opts.maxRegressionPct, "max-regression-pct", 0, "Fail when current pass rate drops more than N percentage points below the baseline (0 = any drop fails)")
	cmd.Flags().BoolVar(&opts.goldenMustPass, "golden-must-pass", true, "Fail with exit code 2 when any task marked golden: true did not pass in the current run")
	cmd.Flags().StringVar(&onNew, "on-new-tasks", "allow", "Policy for tasks present in current but not baseline: allow|warn|fail")
	cmd.Flags().StringVar(&onRemoved, "on-removed-tasks", "warn", "Policy for tasks present in baseline but not current: allow|warn|fail")
	cmd.Flags().StringVarP(&format, "format", "f", "human", "Output format: human|json|markdown|github-actions")
	cmd.Flags().StringVar(&opts.summaryFile, "summary-file", "", "Optional path to write a markdown summary (defaults to $GITHUB_STEP_SUMMARY for --format=github-actions)")

	return cmd
}

func parseGatePolicy(value, flagName string) (gatePolicy, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "allow":
		return gatePolicyAllow, nil
	case "warn":
		return gatePolicyWarn, nil
	case "fail":
		return gatePolicyFail, nil
	default:
		return "", fmt.Errorf("%s: must be one of allow|warn|fail, got %q", flagName, value)
	}
}

func parseGateFormat(value string) (gateFormat, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "human", "":
		return gateFormatHuman, nil
	case "json":
		return gateFormatJSON, nil
	case "markdown", "md":
		return gateFormatMarkdown, nil
	case "github-actions", "gha":
		return gateFormatGithubActions, nil
	default:
		return "", fmt.Errorf("--format: must be one of human|json|markdown|github-actions, got %q", value)
	}
}

// runGate is the testable core of the gate command. It loads files, builds
// the report, renders the chosen format, and returns either nil (pass) or
// a *gateExitError carrying the documented exit code.
func runGate(stdout, stderr io.Writer, opts *gateOpts) error {
	baseline, err := loadOutcomeFile(opts.baselinePath)
	if err != nil {
		return &gateExitError{code: GateExitConfErr, msg: fmt.Sprintf("failed to load baseline %s: %v", opts.baselinePath, err)}
	}
	current, err := loadOutcomeFile(opts.currentPath)
	if err != nil {
		return &gateExitError{code: GateExitConfErr, msg: fmt.Sprintf("failed to load current %s: %v", opts.currentPath, err)}
	}

	report := buildGateReport(baseline, current, opts)

	switch opts.format {
	case gateFormatJSON:
		if err := renderGateJSON(stdout, report); err != nil {
			return &gateExitError{code: GateExitConfErr, msg: err.Error()}
		}
	case gateFormatMarkdown:
		renderGateMarkdown(stdout, report)
	case gateFormatGithubActions:
		renderGateGitHubActions(stdout, stderr, report)
		// Optionally write a job summary to $GITHUB_STEP_SUMMARY or
		// the explicit --summary-file path.
		if err := writeGateSummary(opts, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: failed to write job summary: %v\n", err)
		}
	default:
		renderGateHuman(stdout, report)
	}

	if report.ExitCode == GateExitPass {
		return nil
	}
	return &gateExitError{code: report.ExitCode, msg: gateExitMessage(report)}
}

func gateExitMessage(r *gateReport) string {
	switch r.Outcome {
	case "golden-fail":
		return fmt.Sprintf("gate failed: %d golden task(s) failed: %s", len(r.GoldenFailures), strings.Join(r.GoldenFailures, ", "))
	case "regression":
		return fmt.Sprintf("gate failed: pass-rate regression %.2fpp exceeds threshold %.2fpp", -r.PassRateDelta, r.MaxRegressionPct)
	default:
		return "gate failed"
	}
}

// buildGateReport applies the gate policies to baseline/current and
// returns a populated report. It is the single source of truth for which
// exit code to use: callers in CI can re-derive the verdict from the JSON
// payload without re-running the gate logic.
func buildGateReport(baseline, current *models.EvaluationOutcome, opts *gateOpts) *gateReport {
	r := &gateReport{
		BaselineFile:     opts.baselinePath,
		CurrentFile:      opts.currentPath,
		BaselineModel:    baseline.Setup.ModelID,
		CurrentModel:     current.Setup.ModelID,
		BaselinePassRate: baseline.Digest.SuccessRate,
		CurrentPassRate:  current.Digest.SuccessRate,
		MaxRegressionPct: opts.maxRegressionPct,
		GoldenMustPass:   opts.goldenMustPass,
		OnNewTasks:       string(opts.onNewTasks),
		OnRemovedTasks:   string(opts.onRemovedTasks),
	}
	// pass-rate delta as percentage points (e.g. -5.0 means 5pp drop).
	r.PassRateDelta = (current.Digest.SuccessRate - baseline.Digest.SuccessRate) * 100.0

	// Index both result sets by TestID and collect the union of task IDs
	// in a stable order (baseline first, then current-only).
	baseIdx := indexByTestID(baseline.TestOutcomes)
	currIdx := indexByTestID(current.TestOutcomes)

	ordered := make([]string, 0, len(baseIdx)+len(currIdx))
	seen := make(map[string]bool, len(baseIdx)+len(currIdx))
	for _, t := range baseline.TestOutcomes {
		if !seen[t.TestID] {
			seen[t.TestID] = true
			ordered = append(ordered, t.TestID)
		}
	}
	for _, t := range current.TestOutcomes {
		if !seen[t.TestID] {
			seen[t.TestID] = true
			ordered = append(ordered, t.TestID)
		}
	}

	for _, id := range ordered {
		b, hasBase := baseIdx[id]
		c, hasCurr := currIdx[id]

		row := gateTaskRow{
			TestID:     id,
			InBaseline: hasBase,
			InCurrent:  hasCurr,
		}
		switch {
		case hasCurr:
			row.DisplayName = c.DisplayName
			row.Golden = c.Golden
			row.CurrentStatus = string(c.Status)
			row.CurrentPass = passRateOf(c)
			row.CurrentScore = avgScoreOf(c)
		case hasBase:
			row.DisplayName = b.DisplayName
			row.Golden = b.Golden
		}
		if hasBase {
			if row.DisplayName == "" {
				row.DisplayName = b.DisplayName
			}
			row.BaselineStatus = string(b.Status)
			row.BaselinePass = passRateOf(b)
			row.BaselineScore = avgScoreOf(b)
			// Golden flag propagates from either side; baseline wins if
			// current omitted it.
			if !row.Golden {
				row.Golden = b.Golden
			}
		}

		switch {
		case hasBase && !hasCurr:
			row.Classification = "removed"
			r.RemovedTasks = append(r.RemovedTasks, id)
		case !hasBase && hasCurr:
			row.Classification = "new"
			r.NewTasks = append(r.NewTasks, id)
		default:
			row.PassRateDelta = row.CurrentPass - row.BaselinePass
			row.ScoreDelta = row.CurrentScore - row.BaselineScore
			switch {
			case row.Golden && c.Status != models.StatusPassed:
				row.Classification = "golden-fail"
				row.Note = "golden task did not pass"
			case row.PassRateDelta < 0:
				row.Classification = "regressed"
			case row.PassRateDelta > 0:
				row.Classification = "improved"
				r.Improvements = append(r.Improvements, id)
			default:
				row.Classification = "passed"
			}
			if row.Classification == "regressed" {
				r.Regressions = append(r.Regressions, id)
			}
		}

		// Golden hard-fail check is independent of pass-rate delta and
		// applies to any task that is present in the current run.
		if hasCurr && row.Golden && c.Status != models.StatusPassed && opts.goldenMustPass {
			if row.Classification != "golden-fail" {
				row.Classification = "golden-fail"
			}
			if row.Note == "" {
				row.Note = "golden task did not pass"
			}
			if !containsString(r.GoldenFailures, id) {
				r.GoldenFailures = append(r.GoldenFailures, id)
			}
		}

		r.Tasks = append(r.Tasks, row)
	}

	sort.Strings(r.NewTasks)
	sort.Strings(r.RemovedTasks)
	sort.Strings(r.GoldenFailures)
	sort.Strings(r.Regressions)
	sort.Strings(r.Improvements)

	// Decide overall outcome and exit code. Order matters: golden failure
	// dominates over aggregate regression, so CI knows the task list itself
	// is broken even if the headline pass rate looks fine.
	r.Outcome = "pass"
	r.ExitCode = GateExitPass

	if len(r.GoldenFailures) > 0 && opts.goldenMustPass {
		r.Outcome = "golden-fail"
		r.ExitCode = GateExitGolden
	}

	// Aggregate regression check: only when --max-regression-pct is set
	// (>= 0). The drop is expressed in percentage points of pass rate.
	regressionDrop := -r.PassRateDelta // positive when current < baseline
	if regressionDrop > opts.maxRegressionPct {
		if r.ExitCode == GateExitPass {
			r.Outcome = "regression"
			r.ExitCode = GateExitRegress
		}
	}

	// Task-set delta policies. Warnings always surface; "fail" promotes
	// the exit code if nothing more severe has fired yet.
	applyTaskSetPolicy(r, "new", r.NewTasks, opts.onNewTasks)
	applyTaskSetPolicy(r, "removed", r.RemovedTasks, opts.onRemovedTasks)

	return r
}

func applyTaskSetPolicy(r *gateReport, label string, ids []string, policy gatePolicy) {
	if len(ids) == 0 {
		return
	}
	switch policy {
	case gatePolicyAllow:
		// silent
	case gatePolicyWarn:
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d %s task(s): %s", len(ids), label, strings.Join(ids, ", ")))
	case gatePolicyFail:
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d %s task(s) (policy=fail): %s", len(ids), label, strings.Join(ids, ", ")))
		if r.ExitCode == GateExitPass {
			r.Outcome = "regression"
			r.ExitCode = GateExitRegress
		}
	}
}

func indexByTestID(outcomes []models.TestOutcome) map[string]models.TestOutcome {
	m := make(map[string]models.TestOutcome, len(outcomes))
	for _, o := range outcomes {
		m[o.TestID] = o
	}
	return m
}

func passRateOf(o models.TestOutcome) float64 {
	if o.Stats != nil {
		return o.Stats.PassRate
	}
	if o.Status == models.StatusPassed {
		return 1.0
	}
	return 0.0
}

func avgScoreOf(o models.TestOutcome) float64 {
	if o.Stats != nil {
		return o.Stats.AvgScore
	}
	return 0.0
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// --- renderers -------------------------------------------------------------

func renderGateJSON(w io.Writer, r *gateReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal gate report: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func renderGateHuman(w io.Writer, r *gateReport) {
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 70))
	_, _ = fmt.Fprintln(w, " WAZA GATE")
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 70))
	_, _ = fmt.Fprintf(w, "  baseline: %s (model=%s)\n", r.BaselineFile, r.BaselineModel)
	_, _ = fmt.Fprintf(w, "  current:  %s (model=%s)\n", r.CurrentFile, r.CurrentModel)
	_, _ = fmt.Fprintf(w, "  pass rate: %.1f%% → %.1f%%  (Δ %+.2fpp, threshold -%.2fpp)\n",
		r.BaselinePassRate*100, r.CurrentPassRate*100, r.PassRateDelta, r.MaxRegressionPct)
	_, _ = fmt.Fprintln(w)

	if len(r.GoldenFailures) > 0 {
		_, _ = fmt.Fprintln(w, "  ✗ GOLDEN FAILURES:")
		for _, id := range r.GoldenFailures {
			_, _ = fmt.Fprintf(w, "    - %s\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}
	if len(r.Regressions) > 0 {
		_, _ = fmt.Fprintln(w, "  ↓ Regressed tasks:")
		for _, id := range r.Regressions {
			_, _ = fmt.Fprintf(w, "    - %s\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}
	if len(r.NewTasks) > 0 {
		_, _ = fmt.Fprintf(w, "  + New tasks (policy=%s): %s\n", r.OnNewTasks, strings.Join(r.NewTasks, ", "))
	}
	if len(r.RemovedTasks) > 0 {
		_, _ = fmt.Fprintf(w, "  - Removed tasks (policy=%s): %s\n", r.OnRemovedTasks, strings.Join(r.RemovedTasks, ", "))
	}
	if len(r.Warnings) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, msg := range r.Warnings {
			_, _ = fmt.Fprintf(w, "  warning: %s\n", msg)
		}
	}

	_, _ = fmt.Fprintln(w)
	switch r.Outcome {
	case "pass":
		_, _ = fmt.Fprintln(w, "  RESULT: ✓ PASS")
	case "regression":
		_, _ = fmt.Fprintln(w, "  RESULT: ✗ REGRESSION")
	case "golden-fail":
		_, _ = fmt.Fprintln(w, "  RESULT: ✗ GOLDEN FAILURE")
	}
	_, _ = fmt.Fprintf(w, "  exit code: %d\n", r.ExitCode)
}

func renderGateMarkdown(w io.Writer, r *gateReport) {
	_, _ = fmt.Fprintln(w, "# Waza Gate Report")
	_, _ = fmt.Fprintln(w)
	icon := "✅"
	switch r.Outcome {
	case "regression":
		icon = "❌"
	case "golden-fail":
		icon = "🛑"
	}
	_, _ = fmt.Fprintf(w, "**%s Result:** `%s` (exit %d)\n\n", icon, r.Outcome, r.ExitCode)
	_, _ = fmt.Fprintln(w, "| Metric | Baseline | Current | Δ |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|")
	_, _ = fmt.Fprintf(w, "| Pass rate | %.1f%% | %.1f%% | %+.2fpp |\n",
		r.BaselinePassRate*100, r.CurrentPassRate*100, r.PassRateDelta)
	_, _ = fmt.Fprintf(w, "| Threshold | | | -%.2fpp |\n", r.MaxRegressionPct)
	_, _ = fmt.Fprintln(w)

	if len(r.GoldenFailures) > 0 {
		_, _ = fmt.Fprintln(w, "## 🛑 Golden Failures")
		_, _ = fmt.Fprintln(w)
		for _, id := range r.GoldenFailures {
			_, _ = fmt.Fprintf(w, "- `%s`\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}
	if len(r.Regressions) > 0 {
		_, _ = fmt.Fprintln(w, "## ⬇️ Regressed Tasks")
		_, _ = fmt.Fprintln(w)
		for _, id := range r.Regressions {
			_, _ = fmt.Fprintf(w, "- `%s`\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}
	if len(r.NewTasks) > 0 {
		_, _ = fmt.Fprintf(w, "## ➕ New Tasks (policy: `%s`)\n\n", r.OnNewTasks)
		for _, id := range r.NewTasks {
			_, _ = fmt.Fprintf(w, "- `%s`\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}
	if len(r.RemovedTasks) > 0 {
		_, _ = fmt.Fprintf(w, "## ➖ Removed Tasks (policy: `%s`)\n\n", r.OnRemovedTasks)
		for _, id := range r.RemovedTasks {
			_, _ = fmt.Fprintf(w, "- `%s`\n", id)
		}
		_, _ = fmt.Fprintln(w)
	}

	_, _ = fmt.Fprintln(w, "## Tasks")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "| Task | Golden | Baseline | Current | Δ pass | Status |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|---|---|")
	for _, t := range r.Tasks {
		golden := ""
		if t.Golden {
			golden = "⭐"
		}
		_, _ = fmt.Fprintf(w, "| `%s` | %s | %s | %s | %+.2fpp | %s |\n",
			t.TestID, golden,
			renderStatus(t.BaselineStatus, t.InBaseline),
			renderStatus(t.CurrentStatus, t.InCurrent),
			t.PassRateDelta*100, t.Classification)
	}
}

func renderStatus(s string, present bool) string {
	if !present {
		return "—"
	}
	if s == "" {
		return "?"
	}
	return s
}

// renderGateGitHubActions emits human output plus inline ::error::/::warning::
// annotations consumed by GitHub Actions. Stderr is used for the workflow
// commands so they remain visible even if stdout is redirected.
func renderGateGitHubActions(stdout, stderr io.Writer, r *gateReport) {
	renderGateHuman(stdout, r)

	for _, id := range r.GoldenFailures {
		_, _ = fmt.Fprintf(stderr, "::error title=Golden task failed::%s did not pass in current results\n", id)
	}
	for _, id := range r.Regressions {
		_, _ = fmt.Fprintf(stderr, "::error title=Task regressed::%s pass rate dropped\n", id)
	}
	for _, msg := range r.Warnings {
		_, _ = fmt.Fprintf(stderr, "::warning title=Gate warning::%s\n", msg)
	}
	if r.Outcome != "pass" && len(r.GoldenFailures) == 0 && len(r.Regressions) == 0 {
		_, _ = fmt.Fprintf(stderr, "::error title=Waza gate failed::%s\n", r.Outcome)
	}
}

// writeGateSummary writes the markdown rendering of the report to either
// the explicit --summary-file or, when GITHUB_STEP_SUMMARY is set and the
// format is github-actions, that file. Safe to call even when neither is
// configured: it becomes a no-op.
func writeGateSummary(opts *gateOpts, r *gateReport) error {
	target := opts.summaryFile
	if target == "" && opts.format == gateFormatGithubActions {
		target = os.Getenv("GITHUB_STEP_SUMMARY")
	}
	if target == "" {
		return nil
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	renderGateMarkdown(f, r)
	return nil
}
