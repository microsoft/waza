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

const (
	ExitGateRegression    = 1
	ExitGateGoldenFailure = 2
	ExitGateConfigError   = 3
)

type gateExitError struct {
	code    int
	message string
}

func (e *gateExitError) Error() string {
	return e.message
}

func (e *gateExitError) ExitCode() int {
	return e.code
}

type gateTaskDeltaPolicy string

const (
	gateTaskDeltaAllow gateTaskDeltaPolicy = "allow"
	gateTaskDeltaWarn  gateTaskDeltaPolicy = "warn"
	gateTaskDeltaFail  gateTaskDeltaPolicy = "fail"
)

type gateOutputFormat string

const (
	gateFormatHuman         gateOutputFormat = "human"
	gateFormatJSON          gateOutputFormat = "json"
	gateFormatMarkdown      gateOutputFormat = "markdown"
	gateFormatGitHubActions gateOutputFormat = "github-actions"
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

type gateReport struct {
	Passed                 bool             `json:"passed"`
	ExitCode               int              `json:"exit_code"`
	Verdict                string           `json:"verdict"`
	BaselineFile           string           `json:"baseline_file"`
	CurrentFile            string           `json:"current_file"`
	BaselineSuccessRate    float64          `json:"baseline_success_rate"`
	CurrentSuccessRate     float64          `json:"current_success_rate"`
	RegressionPct          float64          `json:"regression_pct"`
	MaxRegressionPct       float64          `json:"max_regression_pct"`
	GoldenMustPass         bool             `json:"golden_must_pass"`
	GoldenFailures         []gateTaskResult `json:"golden_failures,omitempty"`
	NewTasks               []gateTaskResult `json:"new_tasks,omitempty"`
	RemovedTasks           []gateTaskResult `json:"removed_tasks,omitempty"`
	Warnings               []gateIssue      `json:"warnings,omitempty"`
	Failures               []gateIssue      `json:"failures,omitempty"`
	BaselineTaskCount      int              `json:"baseline_task_count"`
	CurrentTaskCount       int              `json:"current_task_count"`
	NewTaskPolicy          string           `json:"new_task_policy"`
	RemovedTaskPolicy      string           `json:"removed_task_policy"`
	BaselineModel          string           `json:"baseline_model,omitempty"`
	CurrentModel           string           `json:"current_model,omitempty"`
	BaselineRunsPerTest    int              `json:"baseline_runs_per_test,omitempty"`
	CurrentRunsPerTest     int              `json:"current_runs_per_test,omitempty"`
	BaselineAggregateScore float64          `json:"baseline_aggregate_score"`
	CurrentAggregateScore  float64          `json:"current_aggregate_score"`
}

type gateTaskResult struct {
	TaskID      string        `json:"task_id"`
	DisplayName string        `json:"display_name"`
	Golden      bool          `json:"golden,omitempty"`
	Status      models.Status `json:"status"`
	PassRate    float64       `json:"pass_rate"`
}

type gateIssue struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	TaskID   string `json:"task_id,omitempty"`
	TaskName string `json:"task_name,omitempty"`
}

func newGateCommand() *cobra.Command {
	opts := gateOptions{
		onNewTasks:     string(gateTaskDeltaAllow),
		onRemovedTasks: string(gateTaskDeltaWarn),
		format:         string(gateFormatHuman),
	}

	cmd := &cobra.Command{
		Use:   "gate --baseline baseline.json --current results.json",
		Short: "Gate current evaluation results against a baseline",
		Long: `Gate current evaluation results against a baseline for CI.

The gate fails when aggregate pass rate regresses beyond the configured
threshold, when golden tasks fail with --golden-must-pass, or when task set
changes violate the selected added/removed task policies.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return gateConfigError(fmt.Errorf("unexpected positional arguments: %s", strings.Join(args, ", ")))
			}
			return gateCommandE(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.baselinePath, "baseline", "", "Baseline results JSON file")
	cmd.Flags().StringVar(&opts.currentPath, "current", "", "Current results JSON file")
	cmd.Flags().Float64Var(&opts.maxRegressionPct, "max-regression-pct", 0, "Maximum allowed aggregate pass-rate regression in percentage points")
	cmd.Flags().BoolVar(&opts.goldenMustPass, "golden-must-pass", false, "Fail with exit code 2 if any current golden task fails")
	cmd.Flags().StringVar(&opts.onNewTasks, "on-new-tasks", opts.onNewTasks, "Behavior for tasks present only in current results: allow, warn, or fail")
	cmd.Flags().StringVar(&opts.onRemovedTasks, "on-removed-tasks", opts.onRemovedTasks, "Behavior for tasks present only in baseline results: allow, warn, or fail")
	cmd.Flags().StringVarP(&opts.format, "format", "f", opts.format, "Output format: human, json, markdown, or github-actions")

	return cmd
}

func gateCommandE(cmd *cobra.Command, opts gateOptions) error {
	normalized, err := normalizeGateOptions(opts)
	if err != nil {
		return gateConfigError(err)
	}

	baseline, err := loadOutcomeFile(normalized.baselinePath)
	if err != nil {
		return gateConfigError(fmt.Errorf("failed to load baseline %s: %w", normalized.baselinePath, err))
	}
	current, err := loadOutcomeFile(normalized.currentPath)
	if err != nil {
		return gateConfigError(fmt.Errorf("failed to load current %s: %w", normalized.currentPath, err))
	}

	report := buildGateReport(normalized, baseline, current)
	if err := printGateReport(cmd.OutOrStdout(), normalized, report); err != nil {
		return gateConfigError(err)
	}
	if report.Passed {
		return nil
	}
	return &gateExitError{code: report.ExitCode, message: gateFailureMessage(report)}
}

func gateConfigError(err error) error {
	return &gateExitError{code: ExitGateConfigError, message: err.Error()}
}

func normalizeGateOptions(opts gateOptions) (gateOptions, error) {
	opts.baselinePath = strings.TrimSpace(opts.baselinePath)
	opts.currentPath = strings.TrimSpace(opts.currentPath)
	opts.onNewTasks = strings.ToLower(strings.TrimSpace(opts.onNewTasks))
	opts.onRemovedTasks = strings.ToLower(strings.TrimSpace(opts.onRemovedTasks))
	opts.format = strings.ToLower(strings.TrimSpace(opts.format))

	if opts.baselinePath == "" {
		return opts, fmt.Errorf("--baseline is required")
	}
	if opts.currentPath == "" {
		return opts, fmt.Errorf("--current is required")
	}
	if opts.maxRegressionPct < 0 {
		return opts, fmt.Errorf("--max-regression-pct must be >= 0")
	}
	if _, err := parseGateTaskPolicy(opts.onNewTasks); err != nil {
		return opts, fmt.Errorf("--on-new-tasks: %w", err)
	}
	if _, err := parseGateTaskPolicy(opts.onRemovedTasks); err != nil {
		return opts, fmt.Errorf("--on-removed-tasks: %w", err)
	}
	if _, err := parseGateFormat(opts.format); err != nil {
		return opts, err
	}
	return opts, nil
}

func parseGateTaskPolicy(value string) (gateTaskDeltaPolicy, error) {
	switch gateTaskDeltaPolicy(value) {
	case gateTaskDeltaAllow, gateTaskDeltaWarn, gateTaskDeltaFail:
		return gateTaskDeltaPolicy(value), nil
	default:
		return "", fmt.Errorf("unsupported policy %q: must be allow, warn, or fail", value)
	}
}

func parseGateFormat(value string) (gateOutputFormat, error) {
	switch gateOutputFormat(value) {
	case gateFormatHuman, gateFormatJSON, gateFormatMarkdown, gateFormatGitHubActions:
		return gateOutputFormat(value), nil
	default:
		return "", fmt.Errorf("unsupported format %q: must be human, json, markdown, or github-actions", value)
	}
}

func buildGateReport(opts gateOptions, baseline, current *models.EvaluationOutcome) *gateReport {
	newPolicy, _ := parseGateTaskPolicy(opts.onNewTasks)
	removedPolicy, _ := parseGateTaskPolicy(opts.onRemovedTasks)

	report := &gateReport{
		Passed:                 true,
		ExitCode:               ExitSuccess,
		Verdict:                "pass",
		BaselineFile:           opts.baselinePath,
		CurrentFile:            opts.currentPath,
		BaselineSuccessRate:    baseline.Digest.SuccessRate,
		CurrentSuccessRate:     current.Digest.SuccessRate,
		MaxRegressionPct:       opts.maxRegressionPct,
		GoldenMustPass:         opts.goldenMustPass,
		BaselineTaskCount:      len(baseline.TestOutcomes),
		CurrentTaskCount:       len(current.TestOutcomes),
		NewTaskPolicy:          string(newPolicy),
		RemovedTaskPolicy:      string(removedPolicy),
		BaselineModel:          baseline.Setup.ModelID,
		CurrentModel:           current.Setup.ModelID,
		BaselineRunsPerTest:    baseline.Setup.RunsPerTest,
		CurrentRunsPerTest:     current.Setup.RunsPerTest,
		BaselineAggregateScore: baseline.Digest.AggregateScore,
		CurrentAggregateScore:  current.Digest.AggregateScore,
	}

	drop := (baseline.Digest.SuccessRate - current.Digest.SuccessRate) * 100
	if drop > 0 {
		report.RegressionPct = drop
	}
	if report.RegressionPct > opts.maxRegressionPct {
		report.Failures = append(report.Failures, gateIssue{
			Kind:     "regression",
			Severity: "error",
			Message:  fmt.Sprintf("aggregate pass rate regressed by %.2f percentage points, exceeding %.2f", report.RegressionPct, opts.maxRegressionPct),
		})
	}

	baselineTasks := indexGateTasks(baseline.TestOutcomes)
	currentTasks := indexGateTasks(current.TestOutcomes)

	for _, id := range sortedTaskIDs(currentTasks) {
		currentTask := currentTasks[id]
		if _, ok := baselineTasks[id]; !ok {
			task := gateTaskFromOutcome(currentTask)
			report.NewTasks = append(report.NewTasks, task)
			applyGateTaskPolicy(&report.Warnings, &report.Failures, newPolicy, "new_task", task)
		}
		if opts.goldenMustPass && currentTask.Golden && currentTask.Status != models.StatusPassed {
			task := gateTaskFromOutcome(currentTask)
			report.GoldenFailures = append(report.GoldenFailures, task)
		}
	}

	for _, id := range sortedTaskIDs(baselineTasks) {
		if _, ok := currentTasks[id]; ok {
			continue
		}
		task := gateTaskFromOutcome(baselineTasks[id])
		report.RemovedTasks = append(report.RemovedTasks, task)
		applyGateTaskPolicy(&report.Warnings, &report.Failures, removedPolicy, "removed_task", task)
	}

	if len(report.GoldenFailures) > 0 {
		report.Passed = false
		report.ExitCode = ExitGateGoldenFailure
		report.Verdict = "golden-failure"
		report.Failures = appendGoldenFailures(report.Failures, report.GoldenFailures)
		return report
	}

	if len(report.Failures) > 0 {
		report.Passed = false
		report.ExitCode = ExitGateRegression
		report.Verdict = "fail"
	}

	return report
}

func indexGateTasks(outcomes []models.TestOutcome) map[string]models.TestOutcome {
	result := make(map[string]models.TestOutcome, len(outcomes))
	for _, outcome := range outcomes {
		result[outcome.TestID] = outcome
	}
	return result
}

func sortedTaskIDs(tasks map[string]models.TestOutcome) []string {
	ids := make([]string, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func gateTaskFromOutcome(outcome models.TestOutcome) gateTaskResult {
	passRate := 0.0
	if outcome.Stats != nil {
		passRate = outcome.Stats.PassRate
	}
	return gateTaskResult{
		TaskID:      outcome.TestID,
		DisplayName: outcome.DisplayName,
		Golden:      outcome.Golden,
		Status:      outcome.Status,
		PassRate:    passRate,
	}
}

func applyGateTaskPolicy(warnings, failures *[]gateIssue, policy gateTaskDeltaPolicy, kind string, task gateTaskResult) {
	if policy == gateTaskDeltaAllow {
		return
	}
	severity := "warning"
	target := warnings
	if policy == gateTaskDeltaFail {
		severity = "error"
		target = failures
	}
	*target = append(*target, gateIssue{
		Kind:     kind,
		Severity: severity,
		Message:  gateTaskPolicyMessage(kind, task, policy),
		TaskID:   task.TaskID,
		TaskName: task.DisplayName,
	})
}

func gateTaskPolicyMessage(kind string, task gateTaskResult, policy gateTaskDeltaPolicy) string {
	action := "is present only in current results"
	if kind == "removed_task" {
		action = "is present only in baseline results"
	}
	return fmt.Sprintf("task %q (%s) %s and policy is %s", task.DisplayName, task.TaskID, action, policy)
}

func appendGoldenFailures(failures []gateIssue, goldenFailures []gateTaskResult) []gateIssue {
	for _, task := range goldenFailures {
		failures = append(failures, gateIssue{
			Kind:     "golden_failure",
			Severity: "error",
			Message:  fmt.Sprintf("golden task %q (%s) did not pass; status=%s", task.DisplayName, task.TaskID, task.Status),
			TaskID:   task.TaskID,
			TaskName: task.DisplayName,
		})
	}
	return failures
}

func gateFailureMessage(report *gateReport) string {
	switch report.ExitCode {
	case ExitGateGoldenFailure:
		return fmt.Sprintf("gate failed: %d golden task(s) failed", len(report.GoldenFailures))
	case ExitGateRegression:
		return fmt.Sprintf("gate failed: %d failure(s)", len(report.Failures))
	default:
		return "gate failed"
	}
}

func printGateReport(w io.Writer, opts gateOptions, report *gateReport) error {
	format, err := parseGateFormat(opts.format)
	if err != nil {
		return err
	}
	switch format {
	case gateFormatJSON:
		return printGateJSON(w, report)
	case gateFormatMarkdown:
		_, err := fmt.Fprint(w, gateMarkdown(report))
		return err
	case gateFormatGitHubActions:
		return printGateGitHubActions(w, report)
	default:
		return printGateHuman(w, report)
	}
}

func printGateJSON(w io.Writer, report *gateReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal gate report: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

func printGateHuman(w io.Writer, report *gateReport) error {
	status := strings.ToUpper(report.Verdict)
	if report.Passed {
		status = "PASS"
	}
	_, err := fmt.Fprintf(w, "Waza gate: %s\n\n", status)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "Baseline: %.1f%% pass rate (%d tasks, model: %s)\n", report.BaselineSuccessRate*100, report.BaselineTaskCount, emptyAsNA(report.BaselineModel))
	_, _ = fmt.Fprintf(w, "Current:  %.1f%% pass rate (%d tasks, model: %s)\n", report.CurrentSuccessRate*100, report.CurrentTaskCount, emptyAsNA(report.CurrentModel))
	_, _ = fmt.Fprintf(w, "Regression: %.2f percentage points (max allowed %.2f)\n", report.RegressionPct, report.MaxRegressionPct)
	if report.GoldenMustPass {
		_, _ = fmt.Fprintf(w, "Golden tasks: %d failure(s)\n", len(report.GoldenFailures))
	}
	_, _ = fmt.Fprintf(w, "Task changes: %d new, %d removed\n", len(report.NewTasks), len(report.RemovedTasks))

	printGateIssues(w, "Failures", report.Failures)
	printGateIssues(w, "Warnings", report.Warnings)
	return nil
}

func printGateIssues(w io.Writer, title string, issues []gateIssue) {
	if len(issues) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\n%s:\n", title)
	for _, issue := range issues {
		_, _ = fmt.Fprintf(w, "- %s\n", issue.Message)
	}
}

func gateMarkdown(report *gateReport) string {
	var b strings.Builder
	status := "PASS"
	if !report.Passed {
		status = "FAIL"
	}
	_, _ = fmt.Fprintf(&b, "## Waza gate: %s\n\n", status)
	_, _ = fmt.Fprintf(&b, "| Metric | Baseline | Current |\n")
	_, _ = fmt.Fprintf(&b, "|---|---:|---:|\n")
	_, _ = fmt.Fprintf(&b, "| Pass rate | %.1f%% | %.1f%% |\n", report.BaselineSuccessRate*100, report.CurrentSuccessRate*100)
	_, _ = fmt.Fprintf(&b, "| Aggregate score | %.4f | %.4f |\n", report.BaselineAggregateScore, report.CurrentAggregateScore)
	_, _ = fmt.Fprintf(&b, "| Tasks | %d | %d |\n\n", report.BaselineTaskCount, report.CurrentTaskCount)
	_, _ = fmt.Fprintf(&b, "- Regression: %.2f percentage points (max allowed %.2f)\n", report.RegressionPct, report.MaxRegressionPct)
	_, _ = fmt.Fprintf(&b, "- New tasks: %d (`%s`)\n", len(report.NewTasks), report.NewTaskPolicy)
	_, _ = fmt.Fprintf(&b, "- Removed tasks: %d (`%s`)\n", len(report.RemovedTasks), report.RemovedTaskPolicy)
	if report.GoldenMustPass {
		_, _ = fmt.Fprintf(&b, "- Golden failures: %d\n", len(report.GoldenFailures))
	}
	appendGateIssueMarkdown(&b, "Failures", report.Failures)
	appendGateIssueMarkdown(&b, "Warnings", report.Warnings)
	return b.String()
}

func appendGateIssueMarkdown(b *strings.Builder, title string, issues []gateIssue) {
	if len(issues) == 0 {
		return
	}
	_, _ = fmt.Fprintf(b, "\n### %s\n\n", title)
	for _, issue := range issues {
		_, _ = fmt.Fprintf(b, "- %s\n", issue.Message)
	}
}

func printGateGitHubActions(w io.Writer, report *gateReport) error {
	for _, issue := range report.Warnings {
		_, _ = fmt.Fprintf(w, "::warning title=Waza gate::%s\n", escapeGitHubActions(issue.Message))
	}
	for _, issue := range report.Failures {
		_, _ = fmt.Fprintf(w, "::error title=Waza gate::%s\n", escapeGitHubActions(issue.Message))
	}
	summary := gateMarkdown(report)
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		f, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("writing GITHUB_STEP_SUMMARY: %w", err)
		}
		if _, err := f.WriteString(summary + "\n"); err != nil {
			_ = f.Close()
			return fmt.Errorf("writing GITHUB_STEP_SUMMARY: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("closing GITHUB_STEP_SUMMARY: %w", err)
		}
		return nil
	}
	_, err := fmt.Fprint(w, summary)
	return err
}

func escapeGitHubActions(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		"\r", "%0D",
		"\n", "%0A",
		":", "%3A",
		",", "%2C",
	)
	return replacer.Replace(value)
}

func emptyAsNA(value string) string {
	if value == "" {
		return "n/a"
	}
	return value
}
