package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
)

var compareOutputFormat string

func newCompareCommand() *cobra.Command {
	var releasePolicy, collectionDir string
	var assurance assuranceFlags
	cmd := &cobra.Command{
		Use:   "compare <result1.json> <result2.json> [result3.json ...]",
		Short: "Compare multiple evaluation result files",
		Long: `Compare results from multiple evaluation runs side by side.

Loads two or more result JSON files and generates a comparison report showing
per-task score deltas, pass rate differences, and aggregate statistics.

Explicit --release-policy and --collection-dir instead assess a new paired
collection. Missing, partial, invalid or inconclusive selected evidence exits 1.
This mode does not adopt historical result files or change default comparison.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("release-policy") || cmd.Flags().Changed("collection-dir") || assurance.assessmentSelected(cmd) {
				if len(args) != 0 {
					return &ExitCodeError{Code: 1, Err: fmt.Errorf("selected release policy uses its paired collection, not historical positional result files")}
				}
				return nil
			}
			return cobra.MinimumNArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if assurance.assessmentSelected(cmd) {
				return runAssuredAssessment(cmd, &assurance, releasePolicy, collectionDir, compareOutputFormat)
			}
			if cmd.Flags().Changed("release-policy") || cmd.Flags().Changed("collection-dir") {
				format := compareOutputFormat
				if format == "table" {
					format = "human"
				}
				return runControlledAssessment(cmd.OutOrStdout(), releasePolicy, collectionDir, format)
			}
			return compareCommandE(cmd, args)
		},
	}

	cmd.Flags().StringVarP(&compareOutputFormat, "format", "f", "table", "Output format: table or json")
	cmd.Flags().StringVar(&releasePolicy, "release-policy", "", "Explicit independently versioned release policy; selected rejection/inconclusive/invalid exits 1")
	cmd.Flags().StringVar(&collectionDir, "collection-dir", "", "New paired collection directory required with --release-policy")
	assurance.flags(cmd, true)

	return cmd
}

// Availability reasons explain why a per-file comparison value is null.
const (
	availabilityObserved          = "observed"
	availabilityTaskMissing       = "task_missing"
	availabilityStatsMissing      = "stats_missing"
	availabilityToolEventsMissing = "tool_events_not_recorded"
)

// toolEventsSchemaVersion is the first result schema that records tool_events.
const toolEventsSchemaVersion = "1.1"

// taskComparison holds per-task delta information across result files.
// Scores, pass rates and deltas are null when a value is unavailable;
// Availability is populated only when at least one file lacks a value.
type taskComparison struct {
	TaskID       string          `json:"task_id"`
	DisplayName  string          `json:"display_name"`
	Scores       []*float64      `json:"scores"`
	PassRates    []*float64      `json:"pass_rates"`
	Statuses     []models.Status `json:"statuses"`
	ScoreDelta   *float64        `json:"score_delta"`
	PassDelta    *float64        `json:"pass_rate_delta"`
	Availability []string        `json:"availability,omitempty"`
}

// comparisonReport is the full comparison output.
type comparisonReport struct {
	Files          []string         `json:"files"`
	Models         []string         `json:"models"`
	AggScores      []float64        `json:"aggregate_scores"`
	SuccessRates   []float64        `json:"success_rates"`
	AggScoreDelta  float64          `json:"aggregate_score_delta"`
	SuccessRDelta  float64          `json:"success_rate_delta"`
	TaskDeltas     []taskComparison `json:"task_deltas"`
	TotalTests     []int            `json:"total_tests"`
	DurationsMs    []int64          `json:"durations_ms"`
	DurationDeltaM int64            `json:"duration_delta_ms"`

	// ToolMetrics summarizes per-file tool-use statistics. Added with
	// schemaVersion 1.1 (issue #366). Entries are null for results declaring
	// an older schema, which cannot distinguish absent evidence from no calls.
	ToolMetrics []*toolMetrics `json:"tool_metrics"`
	// ToolMetricsAvailability is populated only when a tool_metrics entry is null.
	ToolMetricsAvailability []string `json:"tool_metrics_availability,omitempty"`
}

// toolMetrics aggregates tool-use stats across all tasks in one outcome file.
type toolMetrics struct {
	TotalCalls         int            `json:"total_calls"`
	TasksWithTools     int            `json:"tasks_with_tools"`
	AvgCallsPerTask    float64        `json:"avg_calls_per_task"`
	SuccessRate        float64        `json:"success_rate"`
	SelectionAccuracy  float64        `json:"selection_accuracy"`
	CallCountHistogram map[string]int `json:"call_count_histogram"`
}

func compareCommandE(_ *cobra.Command, args []string) error {
	if compareOutputFormat != "table" && compareOutputFormat != "json" {
		return fmt.Errorf("unsupported format %q: must be table or json", compareOutputFormat)
	}

	outcomes := make([]*models.EvaluationOutcome, 0, len(args))
	for _, path := range args {
		o, err := loadOutcomeFile(path)
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", path, err)
		}
		outcomes = append(outcomes, o)
	}

	report := buildComparisonReport(args, outcomes)

	if compareOutputFormat == "json" {
		return printComparisonJSON(report)
	}
	printComparisonTable(report)
	return nil
}

func loadOutcomeFile(path string) (*models.EvaluationOutcome, error) {
	return models.LoadEvaluationOutcome(path)
}

func buildComparisonReport(files []string, outcomes []*models.EvaluationOutcome) *comparisonReport {
	report := &comparisonReport{
		Files: files,
	}

	for _, o := range outcomes {
		report.Models = append(report.Models, o.Setup.ModelID)
		report.AggScores = append(report.AggScores, o.Digest.AggregateScore)
		report.SuccessRates = append(report.SuccessRates, o.Digest.SuccessRate)
		report.TotalTests = append(report.TotalTests, o.Digest.TotalTests)
		report.DurationsMs = append(report.DurationsMs, o.Digest.DurationMs)
		if recordsToolEvents(o) {
			tm := computeToolMetrics(o)
			report.ToolMetrics = append(report.ToolMetrics, &tm)
			report.ToolMetricsAvailability = append(report.ToolMetricsAvailability, availabilityObserved)
		} else {
			report.ToolMetrics = append(report.ToolMetrics, nil)
			report.ToolMetricsAvailability = append(report.ToolMetricsAvailability, availabilityToolEventsMissing)
		}
	}
	if allObserved(report.ToolMetricsAvailability) {
		report.ToolMetricsAvailability = nil
	}

	n := len(outcomes)
	report.AggScoreDelta = report.AggScores[n-1] - report.AggScores[0]
	report.SuccessRDelta = report.SuccessRates[n-1] - report.SuccessRates[0]
	report.DurationDeltaM = report.DurationsMs[n-1] - report.DurationsMs[0]

	// Build task-level map keyed by test ID
	type taskKey struct {
		id   string
		name string
	}
	allTasks := make([]taskKey, 0)
	seen := make(map[string]bool)
	for _, o := range outcomes {
		for _, t := range o.TestOutcomes {
			if !seen[t.TestID] {
				seen[t.TestID] = true
				allTasks = append(allTasks, taskKey{id: t.TestID, name: t.DisplayName})
			}
		}
	}

	for _, tk := range allTasks {
		tc := taskComparison{
			TaskID:      tk.id,
			DisplayName: tk.name,
		}
		for _, o := range outcomes {
			var score, passRate *float64
			status := models.StatusNA
			availability := availabilityTaskMissing
			for _, t := range o.TestOutcomes {
				if t.TestID == tk.id {
					status = t.Status
					availability = availabilityStatsMissing
					if t.Stats != nil {
						avg, rate := t.Stats.AvgScore, t.Stats.PassRate
						score, passRate = &avg, &rate
						availability = availabilityObserved
					}
					break
				}
			}
			tc.Scores = append(tc.Scores, score)
			tc.PassRates = append(tc.PassRates, passRate)
			tc.Statuses = append(tc.Statuses, status)
			tc.Availability = append(tc.Availability, availability)
		}
		tc.ScoreDelta = optionalDelta(tc.Scores[0], tc.Scores[n-1])
		tc.PassDelta = optionalDelta(tc.PassRates[0], tc.PassRates[n-1])
		if allObserved(tc.Availability) {
			tc.Availability = nil
		}
		report.TaskDeltas = append(report.TaskDeltas, tc)
	}

	return report
}

// optionalDelta returns to-from, or nil when either operand is unavailable.
func optionalDelta(from, to *float64) *float64 {
	if from == nil || to == nil {
		return nil
	}
	d := *to - *from
	return &d
}

func allObserved(availability []string) bool {
	for _, a := range availability {
		if a != availabilityObserved {
			return false
		}
	}
	return true
}

// recordsToolEvents reports whether a result can distinguish observed zero
// tool calls from absent evidence. Results declaring a schema older than 1.1
// predate tool_events, so missing events there are unavailable, not zero.
func recordsToolEvents(o *models.EvaluationOutcome) bool {
	for _, t := range o.TestOutcomes {
		for _, run := range t.Runs {
			if len(run.ToolEvents) > 0 {
				return true
			}
		}
	}
	return !schemaVersionBefore(o.SchemaVersion, toolEventsSchemaVersion)
}

// schemaVersionBefore reports whether MAJOR.MINOR version a precedes b.
// Empty or unparsable versions are not treated as older.
func schemaVersionBefore(a, b string) bool {
	var aMajor, aMinor, bMajor, bMinor int
	if _, err := fmt.Sscanf(a, "%d.%d", &aMajor, &aMinor); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(b, "%d.%d", &bMajor, &bMinor); err != nil {
		return false
	}
	if aMajor != bMajor {
		return aMajor < bMajor
	}
	return aMinor < bMinor
}

func formatOptional(v *float64, format string) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf(format, *v)
}

func printComparisonTable(r *comparisonReport) {
	n := len(r.Files)

	// Header
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println(" COMPARISON REPORT")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println()

	// File listing
	for i, f := range r.Files {
		fmt.Printf("  [%d] %s  (model: %s)\n", i+1, f, r.Models[i])
	}
	fmt.Println()

	// Aggregate summary
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println(" AGGREGATE")
	fmt.Println(strings.Repeat("-", 70))

	fmt.Printf("  %-20s", "Metric")
	for i := range r.Files {
		fmt.Printf("  [%d]      ", i+1)
	}
	fmt.Printf("  Delta\n")

	fmt.Printf("  %-20s", "Score")
	for _, s := range r.AggScores {
		fmt.Printf("  %-9.4f", s)
	}
	fmt.Printf("  %+.4f\n", r.AggScoreDelta)

	fmt.Printf("  %-20s", "Success Rate")
	for _, s := range r.SuccessRates {
		fmt.Printf("  %-9.1f%%", s*100)
	}
	fmt.Printf("  %+.1f%%\n", r.SuccessRDelta*100)

	fmt.Printf("  %-20s", "Duration (ms)")
	for _, d := range r.DurationsMs {
		fmt.Printf("  %-9d", d)
	}
	fmt.Printf("  %+d\n", r.DurationDeltaM)
	fmt.Println()

	// Tool metrics (additive in schemaVersion 1.1)
	if len(r.ToolMetrics) == n && hasAnyToolData(r.ToolMetrics) {
		fmt.Println(strings.Repeat("-", 70))
		fmt.Println(" TOOL USE")
		fmt.Println(strings.Repeat("-", 70))

		printToolRow("Total calls", r.ToolMetrics, func(tm *toolMetrics) float64 { return float64(tm.TotalCalls) }, "%-9.0f", "%+.0f")
		printToolRow("Tasks w/ tools", r.ToolMetrics, func(tm *toolMetrics) float64 { return float64(tm.TasksWithTools) }, "%-9.0f", "%+.0f")
		printToolRow("Avg calls/task", r.ToolMetrics, func(tm *toolMetrics) float64 { return tm.AvgCallsPerTask }, "%-9.2f", "%+.2f")
		printToolRow("Success rate", r.ToolMetrics, func(tm *toolMetrics) float64 { return tm.SuccessRate * 100 }, "%-9.1f%%", "%+.1f%%")
		printToolRow("Selection accuracy", r.ToolMetrics, func(tm *toolMetrics) float64 { return tm.SelectionAccuracy * 100 }, "%-9.1f%%", "%+.1f%%")
		for _, bucket := range []string{"0", "1", "2", "3+"} {
			printToolRow("Tasks w/ "+bucket+" calls", r.ToolMetrics, func(tm *toolMetrics) float64 {
				return float64(tm.CallCountHistogram[bucket])
			}, "%-9.0f", "%+.0f")
		}
		fmt.Println()
	}

	// Per-task table
	fmt.Println(strings.Repeat("-", 70))
	fmt.Println(" PER-TASK DELTAS")
	fmt.Println(strings.Repeat("-", 70))

	// Column header
	fmt.Printf("  %-25s", "Task")
	for i := range r.Files {
		fmt.Printf("  [%d] Score", i+1)
	}
	fmt.Printf("  Delta\n")

	for _, tc := range r.TaskDeltas {
		name := tc.DisplayName
		if len(name) > 25 {
			name = name[:22] + "..."
		}
		fmt.Printf("  %-25s", name)
		for i := 0; i < n; i++ {
			fmt.Printf("  %-9s", formatOptional(tc.Scores[i], "%.4f"))
		}
		deltaIcon := " "
		if tc.ScoreDelta != nil && *tc.ScoreDelta > 0 {
			deltaIcon = "↑"
		} else if tc.ScoreDelta != nil && *tc.ScoreDelta < 0 {
			deltaIcon = "↓"
		}
		fmt.Printf("  %s%s\n", deltaIcon, formatOptional(tc.ScoreDelta, "%+.4f"))
	}
	fmt.Println()
}

// printToolRow prints one TOOL USE row, rendering unavailable files and
// deltas with an unavailable operand as n/a.
func printToolRow(label string, metrics []*toolMetrics, value func(*toolMetrics) float64, valueFormat, deltaFormat string) {
	values := make([]*float64, len(metrics))
	fmt.Printf("  %-20s", label)
	for i, tm := range metrics {
		if tm != nil {
			v := value(tm)
			values[i] = &v
		}
		if values[i] == nil {
			fmt.Printf("  %-9s", "n/a")
		} else {
			fmt.Printf("  "+valueFormat, *values[i])
		}
	}
	fmt.Printf("  %s\n", formatOptional(optionalDelta(values[0], values[len(values)-1]), deltaFormat))
}

// hasAnyToolData reports whether any file has at least one recorded tool call.
// Suppresses the "TOOL USE" table for purely tool-free outcomes.
func hasAnyToolData(metrics []*toolMetrics) bool {
	for _, m := range metrics {
		if m != nil && m.TotalCalls > 0 {
			return true
		}
	}
	return false
}

func printComparisonJSON(r *comparisonReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal comparison report: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// computeToolMetrics aggregates per-task ToolEvents and tool_calls grader
// outcomes into a single summary record. Tasks without ToolEvents contribute
// zero counts; tasks without a tool_calls grader are excluded from the
// SelectionAccuracy denominator.
func computeToolMetrics(o *models.EvaluationOutcome) toolMetrics {
	hist := map[string]int{"0": 0, "1": 0, "2": 0, "3+": 0}
	tm := toolMetrics{CallCountHistogram: hist}

	var totalCalls, successfulCalls int
	var graderTasks, graderPasses int

	for _, t := range o.TestOutcomes {
		callsThisTask := 0
		successThisTask := 0
		for _, run := range t.Runs {
			runCalls := len(run.ToolEvents)
			callsThisTask += runCalls
			for _, ev := range run.ToolEvents {
				if ev.Success {
					successThisTask++
				}
			}
		}
		// Per-task histogram bucket: one entry per task (not per run), summing
		// tool calls across every trial of that task. This keeps the
		// distribution stable regardless of trials_per_task — a task that
		// invoked two tools across three retries lands in "2" once, not in
		// "0"/"1"/"2"/"3+" multiple times.
		switch callsThisTask {
		case 0:
			hist["0"]++
		case 1:
			hist["1"]++
		case 2:
			hist["2"]++
		default:
			hist["3+"]++
		}
		if callsThisTask > 0 {
			tm.TasksWithTools++
		}
		totalCalls += callsThisTask
		successfulCalls += successThisTask

		// Selection accuracy: did the tool_calls grader pass for this task?
		hasToolCallsGrader := false
		allPassed := true
		for _, run := range t.Runs {
			for _, v := range run.Validations {
				if v.Type != models.GraderKindToolCalls {
					continue
				}
				hasToolCallsGrader = true
				if !v.Passed {
					allPassed = false
				}
			}
		}
		if hasToolCallsGrader {
			graderTasks++
			if allPassed {
				graderPasses++
			}
		}
	}

	tm.TotalCalls = totalCalls
	if len(o.TestOutcomes) > 0 {
		tm.AvgCallsPerTask = float64(totalCalls) / float64(len(o.TestOutcomes))
	}
	if totalCalls > 0 {
		tm.SuccessRate = float64(successfulCalls) / float64(totalCalls)
	}
	if graderTasks > 0 {
		tm.SelectionAccuracy = float64(graderPasses) / float64(graderTasks)
	}
	return tm
}
