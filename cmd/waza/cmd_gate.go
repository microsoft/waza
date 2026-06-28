package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
)

// Documented, stable exit codes for `waza gate`. CI scripts may rely on
// these values — do not change them without a release note.
const (
	// GateExitPass means the candidate met every configured gate.
	GateExitPass = 0
	// GateExitRegression means the aggregate pass rate dropped more than the
	// configured threshold, or a task-set policy was set to fail and tripped.
	GateExitRegression = 1
	// GateExitGoldenFailure means one or more golden tasks did not pass in
	// the current run. Takes precedence over a plain regression.
	GateExitGoldenFailure = 2
	// GateExitConfigError means flags or input files were invalid.
	GateExitConfigError = 3
)

// Task-set delta policies.
const (
	gatePolicyAllow = "allow"
	gatePolicyWarn  = "warn"
	gatePolicyFail  = "fail"
)

// Output formats.
const (
	gateFormatHuman         = "human"
	gateFormatJSON          = "json"
	gateFormatMarkdown      = "markdown"
	gateFormatGitHubActions = "github-actions"
)

type gateOptions struct {
	baselinePath     string
	currentPath      string
	maxRegressionPct float64
	goldenMustPass   bool
	onNewTasks       string
	onRemovedTasks   string
	format           string
}

// GateReport is the machine-readable result of a gate evaluation. It is the
// canonical shape for `--format json` and is also rendered into the other
// formats so downstream tools see a consistent set of fields.
type GateReport struct {
	BaselineFile      string         `json:"baseline_file"`
	CurrentFile       string         `json:"current_file"`
	BaselinePassRate  float64        `json:"baseline_pass_rate"`
	CurrentPassRate   float64        `json:"current_pass_rate"`
	PassRateDelta     float64        `json:"pass_rate_delta"`
	MaxRegressionPct  float64        `json:"max_regression_pct"`
	RegressionPct     float64        `json:"regression_pct"`
	RegressionExceeds bool           `json:"regression_exceeds_threshold"`
	GoldenFailures    []GoldenStatus `json:"golden_failures,omitempty"`
	GoldenTotal       int            `json:"golden_total"`
	GoldenMustPass    bool           `json:"golden_must_pass"`
	NewTasks          []string       `json:"new_tasks,omitempty"`
	RemovedTasks      []string       `json:"removed_tasks,omitempty"`
	OnNewTasks        string         `json:"on_new_tasks"`
	OnRemovedTasks    string         `json:"on_removed_tasks"`
	Warnings          []string       `json:"warnings,omitempty"`
	Errors            []string       `json:"errors,omitempty"`
	ExitCode          int            `json:"exit_code"`
	Outcome           string         `json:"outcome"`
}

// GoldenStatus is a single golden-task summary line for the report.
type GoldenStatus struct {
	TestID         string        `json:"test_id"`
	DisplayName    string        `json:"display_name"`
	BaselineStatus models.Status `json:"baseline_status,omitempty"`
	CurrentStatus  models.Status `json:"current_status"`
}

func newGateCommand() *cobra.Command {
	opts := &gateOptions{
		maxRegressionPct: 0,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyWarn,
		format:           gateFormatHuman,
	}

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Regression gate for CI: compare results to a baseline and fail on regressions",
		Long: `Compare a candidate results.json against a baseline results.json and
fail the build when the candidate regresses beyond configured thresholds.

waza gate is the CI-first counterpart to waza compare. It is opinionated,
produces stable exit codes, and supports CI-native output formats.

Exit codes (stable):
  0  pass — all gates satisfied
  1  regression — aggregate pass rate dropped beyond --max-regression-pct,
     or a task-set policy was set to "fail" and tripped
  2  golden failure — one or more "golden: true" tasks did not pass
  3  configuration error — bad flags or unreadable input files

Golden failures take precedence over plain regressions. Tasks are marked
golden by adding 'golden: true' to the task YAML; the flag is persisted to
results.json so the gate can read it without re-loading YAML.`,
		Example: `  # Fail the build on ANY drop in pass rate (default --max-regression-pct=0),
  # or if any golden task fails.
  waza gate --baseline baseline.json --current results.json

  # Tolerate up to 5pp drop in pass rate.
  waza gate --baseline baseline.json --current results.json --max-regression-pct 5

  # GitHub Actions: emit ::error:: / ::warning:: annotations and a step summary.
  waza gate --baseline baseline.json --current results.json --format github-actions

  # Forbid new tasks (e.g. a frozen suite), tolerate removed ones silently.
  waza gate --baseline baseline.json --current results.json \
    --on-new-tasks fail --on-removed-tasks allow`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGate(cmd.OutOrStdout(), opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.baselinePath, "baseline", "", "Path to the baseline results.json (required)")
	f.StringVar(&opts.currentPath, "current", "", "Path to the candidate results.json (required)")
	f.Float64Var(&opts.maxRegressionPct, "max-regression-pct", opts.maxRegressionPct,
		"Maximum tolerated drop in pass rate, in percentage points (e.g. 5 = 5pp).")
	f.BoolVar(&opts.goldenMustPass, "golden-must-pass", opts.goldenMustPass,
		"Fail with exit code 2 if any task marked 'golden: true' did not pass.")
	f.StringVar(&opts.onNewTasks, "on-new-tasks", opts.onNewTasks,
		"Policy when current adds tasks not in baseline: allow|warn|fail.")
	f.StringVar(&opts.onRemovedTasks, "on-removed-tasks", opts.onRemovedTasks,
		"Policy when current is missing tasks present in baseline: allow|warn|fail.")
	f.StringVarP(&opts.format, "format", "f", opts.format,
		"Output format: human|json|markdown|github-actions.")

	return cmd
}

func runGate(out io.Writer, opts *gateOptions) error {
	if err := validateGateOptions(opts); err != nil {
		return &ExitCodeError{Code: GateExitConfigError, Err: err}
	}

	baseline, err := loadOutcomeFile(opts.baselinePath)
	if err != nil {
		return &ExitCodeError{Code: GateExitConfigError,
			Err: fmt.Errorf("loading baseline %s: %w", opts.baselinePath, err)}
	}
	current, err := loadOutcomeFile(opts.currentPath)
	if err != nil {
		return &ExitCodeError{Code: GateExitConfigError,
			Err: fmt.Errorf("loading current %s: %w", opts.currentPath, err)}
	}

	report := buildGateReport(opts, baseline, current)

	if err := renderGateReport(out, report, opts.format); err != nil {
		return &ExitCodeError{Code: GateExitConfigError, Err: err}
	}

	if report.ExitCode == GateExitPass {
		return nil
	}
	// Render already wrote the full human/markdown/etc. summary. We set
	// SilenceErrors on the cobra command, so Cobra won't echo this string;
	// main.go uses ExitCodeError.Code for the process exit and skips printing
	// when the wrapped error matches the rendered outcome. We still include a
	// short message so callers using runGate directly (tests, library use)
	// receive context.
	return &ExitCodeError{
		Code: report.ExitCode,
		Err:  fmt.Errorf("waza gate: %s (exit %d)", report.Outcome, report.ExitCode),
	}
}

func validateGateOptions(opts *gateOptions) error {
	if strings.TrimSpace(opts.baselinePath) == "" {
		return errors.New("--baseline is required")
	}
	if strings.TrimSpace(opts.currentPath) == "" {
		return errors.New("--current is required")
	}
	if opts.maxRegressionPct < 0 {
		return fmt.Errorf("--max-regression-pct must be >= 0, got %v", opts.maxRegressionPct)
	}
	if !isValidPolicy(opts.onNewTasks) {
		return fmt.Errorf("--on-new-tasks must be one of allow|warn|fail, got %q", opts.onNewTasks)
	}
	if !isValidPolicy(opts.onRemovedTasks) {
		return fmt.Errorf("--on-removed-tasks must be one of allow|warn|fail, got %q", opts.onRemovedTasks)
	}
	switch opts.format {
	case gateFormatHuman, gateFormatJSON, gateFormatMarkdown, gateFormatGitHubActions:
	default:
		return fmt.Errorf("--format must be one of human|json|markdown|github-actions, got %q", opts.format)
	}
	return nil
}

func isValidPolicy(p string) bool {
	switch p {
	case gatePolicyAllow, gatePolicyWarn, gatePolicyFail:
		return true
	}
	return false
}

// buildGateReport applies all gating rules and produces a report with the
// final exit code. It does not perform any I/O.
func buildGateReport(opts *gateOptions, baseline, current *models.EvaluationOutcome) *GateReport {
	r := &GateReport{
		BaselineFile:     opts.baselinePath,
		CurrentFile:      opts.currentPath,
		BaselinePassRate: baseline.Digest.SuccessRate,
		CurrentPassRate:  current.Digest.SuccessRate,
		MaxRegressionPct: opts.maxRegressionPct,
		GoldenMustPass:   opts.goldenMustPass,
		OnNewTasks:       opts.onNewTasks,
		OnRemovedTasks:   opts.onRemovedTasks,
	}

	r.PassRateDelta = r.CurrentPassRate - r.BaselinePassRate
	// Regression is expressed in percentage points (e.g. 0.92 -> 0.85 is 7pp).
	if r.PassRateDelta < 0 {
		r.RegressionPct = -r.PassRateDelta * 100.0
	}
	r.RegressionExceeds = r.RegressionPct > opts.maxRegressionPct

	// Index outcomes by test ID for set diff + golden lookup.
	baseByID := indexOutcomes(baseline)
	currByID := indexOutcomes(current)

	// Task-set delta.
	for id := range currByID {
		if _, ok := baseByID[id]; !ok {
			r.NewTasks = append(r.NewTasks, id)
		}
	}
	for id := range baseByID {
		if _, ok := currByID[id]; !ok {
			r.RemovedTasks = append(r.RemovedTasks, id)
		}
	}
	sort.Strings(r.NewTasks)
	sort.Strings(r.RemovedTasks)

	// Golden evaluation. A task is golden if EITHER side marks it golden;
	// this is conservative — once a task is declared golden it must stay
	// passing across the baseline/current boundary even if the field is
	// missing on the older results.json.
	goldenIDs := map[string]bool{}
	for id, t := range currByID {
		if t.Golden {
			goldenIDs[id] = true
		}
	}
	for id, t := range baseByID {
		if t.Golden {
			goldenIDs[id] = true
		}
	}
	r.GoldenTotal = len(goldenIDs)
	for id := range goldenIDs {
		cur, present := currByID[id]
		if !present {
			r.GoldenFailures = append(r.GoldenFailures, GoldenStatus{
				TestID:         id,
				DisplayName:    baseDisplayName(baseByID, id),
				BaselineStatus: baseByID[id].Status,
				CurrentStatus:  models.StatusNA,
			})
			continue
		}
		if cur.Status != models.StatusPassed {
			gs := GoldenStatus{
				TestID:        id,
				DisplayName:   cur.DisplayName,
				CurrentStatus: cur.Status,
			}
			if b, ok := baseByID[id]; ok {
				gs.BaselineStatus = b.Status
			}
			r.GoldenFailures = append(r.GoldenFailures, gs)
		}
	}
	sort.Slice(r.GoldenFailures, func(i, j int) bool {
		return r.GoldenFailures[i].TestID < r.GoldenFailures[j].TestID
	})

	// Apply policies for new/removed tasks.
	if len(r.NewTasks) > 0 {
		msg := fmt.Sprintf("current adds %d task(s) not in baseline: %s",
			len(r.NewTasks), strings.Join(r.NewTasks, ", "))
		switch opts.onNewTasks {
		case gatePolicyWarn:
			r.Warnings = append(r.Warnings, msg)
		case gatePolicyFail:
			r.Errors = append(r.Errors, msg)
		}
	}
	if len(r.RemovedTasks) > 0 {
		msg := fmt.Sprintf("current is missing %d task(s) present in baseline: %s",
			len(r.RemovedTasks), strings.Join(r.RemovedTasks, ", "))
		switch opts.onRemovedTasks {
		case gatePolicyWarn:
			r.Warnings = append(r.Warnings, msg)
		case gatePolicyFail:
			r.Errors = append(r.Errors, msg)
		}
	}

	// Decide exit code. Order matters: golden > regression > task-set-fail.
	switch {
	case opts.goldenMustPass && len(r.GoldenFailures) > 0:
		r.ExitCode = GateExitGoldenFailure
		r.Outcome = "golden_failure"
	case r.RegressionExceeds:
		r.ExitCode = GateExitRegression
		r.Outcome = "regression"
	case len(r.Errors) > 0:
		r.ExitCode = GateExitRegression
		r.Outcome = "regression"
	default:
		r.ExitCode = GateExitPass
		r.Outcome = "pass"
	}
	return r
}

func indexOutcomes(o *models.EvaluationOutcome) map[string]models.TestOutcome {
	out := make(map[string]models.TestOutcome, len(o.TestOutcomes))
	for _, t := range o.TestOutcomes {
		out[t.TestID] = t
	}
	return out
}

func baseDisplayName(byID map[string]models.TestOutcome, id string) string {
	if t, ok := byID[id]; ok && t.DisplayName != "" {
		return t.DisplayName
	}
	return id
}

// ----- Rendering -----

func renderGateReport(out io.Writer, r *GateReport, format string) error {
	switch format {
	case gateFormatJSON:
		return renderGateJSON(out, r)
	case gateFormatMarkdown:
		renderGateMarkdown(out, r)
		return nil
	case gateFormatGitHubActions:
		renderGateGitHubActions(out, r)
		return nil
	default: // human
		renderGateHuman(out, r)
		return nil
	}
}

func renderGateJSON(out io.Writer, r *GateReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling gate report: %w", err)
	}
	_, _ = fmt.Fprintln(out, string(data))
	return nil
}

// fprintf/fprintln wrap fmt to discard the (n, err) return values; the
// rendering helpers below intentionally write best-effort to caller-supplied
// io.Writers (typically a bytes.Buffer or os.Stdout). Errors here would not
// change the gate's exit code, so silencing them keeps the code readable
// without losing signal.
func fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

func renderGateHuman(out io.Writer, r *GateReport) {
	fprintln(out, strings.Repeat("=", 70))
	fprintln(out, " REGRESSION GATE")
	fprintln(out, strings.Repeat("=", 70))
	fprintf(out, "  baseline: %s\n", r.BaselineFile)
	fprintf(out, "  current:  %s\n", r.CurrentFile)
	fprintln(out)
	fprintf(out, "  pass rate: %.1f%% -> %.1f%%  (delta %+.1fpp, max regression %.1fpp)\n",
		r.BaselinePassRate*100, r.CurrentPassRate*100, r.PassRateDelta*100, r.MaxRegressionPct)
	if r.RegressionExceeds {
		fprintf(out, "  ✗ regression %.2fpp exceeds threshold %.2fpp\n", r.RegressionPct, r.MaxRegressionPct)
	} else {
		fprintf(out, "  ✓ regression within threshold\n")
	}
	fprintln(out)

	fprintf(out, "  golden tasks: %d total", r.GoldenTotal)
	if len(r.GoldenFailures) > 0 {
		fprintf(out, ", %d FAILED\n", len(r.GoldenFailures))
		for _, g := range r.GoldenFailures {
			fprintf(out, "    ✗ %s (%s): %s\n", g.TestID, g.DisplayName, g.CurrentStatus)
		}
	} else {
		fprintln(out, ", all passing")
	}
	fprintln(out)

	if len(r.NewTasks) > 0 {
		fprintf(out, "  new tasks (%s): %s\n", r.OnNewTasks, strings.Join(r.NewTasks, ", "))
	}
	if len(r.RemovedTasks) > 0 {
		fprintf(out, "  removed tasks (%s): %s\n", r.OnRemovedTasks, strings.Join(r.RemovedTasks, ", "))
	}
	if len(r.Warnings) > 0 {
		fprintln(out)
		fprintln(out, "  warnings:")
		for _, w := range r.Warnings {
			fprintf(out, "    ! %s\n", w)
		}
	}
	if len(r.Errors) > 0 {
		fprintln(out)
		fprintln(out, "  errors:")
		for _, e := range r.Errors {
			fprintf(out, "    ✗ %s\n", e)
		}
	}
	fprintln(out)
	fprintln(out, strings.Repeat("-", 70))
	fprintf(out, "  outcome: %s  (exit %d)\n", strings.ToUpper(r.Outcome), r.ExitCode)
	fprintln(out, strings.Repeat("=", 70))
}

func renderGateMarkdown(out io.Writer, r *GateReport) {
	icon := "✅"
	if r.ExitCode != GateExitPass {
		icon = "❌"
	}
	fprintf(out, "## %s Waza regression gate — `%s`\n\n", icon, strings.ToUpper(r.Outcome))
	fprintf(out, "| Metric | Baseline | Current | Delta |\n")
	fprintf(out, "|---|---:|---:|---:|\n")
	fprintf(out, "| Pass rate | %.1f%% | %.1f%% | %+.1fpp |\n",
		r.BaselinePassRate*100, r.CurrentPassRate*100, r.PassRateDelta*100)
	fprintf(out, "\n- Max tolerated regression: **%.1fpp**\n", r.MaxRegressionPct)
	fprintf(out, "- Observed regression: **%.2fpp** %s\n",
		r.RegressionPct, mdCheck(!r.RegressionExceeds))

	fprintf(out, "- Golden tasks: **%d** total, **%d** failed %s\n",
		r.GoldenTotal, len(r.GoldenFailures), mdCheck(len(r.GoldenFailures) == 0))
	if len(r.GoldenFailures) > 0 {
		fprintln(out, "\n### Golden failures")
		fprintln(out)
		fprintln(out, "| Task | Status |")
		fprintln(out, "|---|---|")
		for _, g := range r.GoldenFailures {
			fprintf(out, "| `%s` — %s | `%s` |\n", g.TestID, g.DisplayName, g.CurrentStatus)
		}
	}

	if len(r.NewTasks) > 0 {
		fprintf(out, "\n- New tasks (policy `%s`): %s\n", r.OnNewTasks, mdCodeList(r.NewTasks))
	}
	if len(r.RemovedTasks) > 0 {
		fprintf(out, "- Removed tasks (policy `%s`): %s\n", r.OnRemovedTasks, mdCodeList(r.RemovedTasks))
	}
	if len(r.Warnings) > 0 {
		fprintln(out, "\n**Warnings**")
		for _, w := range r.Warnings {
			fprintf(out, "- ⚠️ %s\n", w)
		}
	}
	if len(r.Errors) > 0 {
		fprintln(out, "\n**Errors**")
		for _, e := range r.Errors {
			fprintf(out, "- ❌ %s\n", e)
		}
	}
	fprintf(out, "\n_Exit code: %d_\n", r.ExitCode)
}

func mdCheck(ok bool) string {
	if ok {
		return "✅"
	}
	return "❌"
}

func mdCodeList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "`" + s + "`"
	}
	return strings.Join(q, ", ")
}

// renderGateGitHubActions emits stdout suitable for inline annotations
// (`::error::`, `::warning::`, `::notice::`), and additionally appends a
// markdown summary to $GITHUB_STEP_SUMMARY when that env var is set.
// This lets a single command power both the PR check and the job summary.
func renderGateGitHubActions(out io.Writer, r *GateReport) {
	if r.RegressionExceeds {
		fprintf(out, "::error title=Waza regression::Pass rate dropped %.2fpp (limit %.2fpp): %.1f%% -> %.1f%%\n",
			r.RegressionPct, r.MaxRegressionPct, r.BaselinePassRate*100, r.CurrentPassRate*100)
	} else {
		fprintf(out, "::notice title=Waza regression gate::Pass rate %.1f%% -> %.1f%% (delta %+.2fpp)\n",
			r.BaselinePassRate*100, r.CurrentPassRate*100, r.PassRateDelta*100)
	}
	// Golden task annotations: ::error:: when the policy enforces them,
	// ::warning:: when --golden-must-pass=false relaxes enforcement.
	goldenLevel := "error"
	goldenTitle := "Golden task failed"
	if !r.GoldenMustPass {
		goldenLevel = "warning"
		goldenTitle = "Golden task failed (non-blocking)"
	}
	for _, g := range r.GoldenFailures {
		fprintf(out, "::%s title=%s::%s (%s) — current status %s\n",
			goldenLevel, goldenTitle, g.TestID, g.DisplayName, g.CurrentStatus)
	}
	for _, w := range r.Warnings {
		fprintf(out, "::warning title=Waza gate::%s\n", sanitizeGHA(w))
	}
	for _, e := range r.Errors {
		fprintf(out, "::error title=Waza gate::%s\n", sanitizeGHA(e))
	}

	// Append a markdown summary to the GitHub Actions step summary file.
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			renderGateMarkdown(f, r)
			_ = f.Close()
		}
	}
}

// sanitizeGHA strips characters that break the workflow command line format.
func sanitizeGHA(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
