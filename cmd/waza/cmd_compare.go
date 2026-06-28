package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
)

var compareOutputFormat string

func newCompareCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare <result1.json> <result2.json> [result3.json ...]",
		Short: "Compare multiple evaluation result files",
		Long: `Compare results from multiple evaluation runs side by side.

Loads two or more result JSON files and generates a comparison report showing
per-task score deltas, pass rate differences, and aggregate statistics.`,
		Args: cobra.MinimumNArgs(2),
		RunE: compareCommandE,
	}

	cmd.Flags().StringVarP(&compareOutputFormat, "format", "f", "table", "Output format: table or json")

	return cmd
}

// taskComparison holds per-task delta information across result files.
type taskComparison struct {
	TaskID      string          `json:"task_id"`
	DisplayName string          `json:"display_name"`
	Scores      []float64       `json:"scores"`
	PassRates   []float64       `json:"pass_rates"`
	Statuses    []models.Status `json:"statuses"`
	ScoreDelta  float64         `json:"score_delta"`
	PassDelta   float64         `json:"pass_rate_delta"`
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
	// schemaVersion 1.1 (issue #366). Files that don't include tool_events
	// produce zero values, which is safe for delta math.
	ToolMetrics []toolMetrics `json:"tool_metrics"`
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
		report.ToolMetrics = append(report.ToolMetrics, computeToolMetrics(o))
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
			found := false
			for _, t := range o.TestOutcomes {
				if t.TestID == tk.id {
					found = true
					score := 0.0
					passRate := 0.0
					if t.Stats != nil {
						score = t.Stats.AvgScore
						passRate = t.Stats.PassRate
					}
					tc.Scores = append(tc.Scores, score)
					tc.PassRates = append(tc.PassRates, passRate)
					tc.Statuses = append(tc.Statuses, t.Status)
					break
				}
			}
			if !found {
				tc.Scores = append(tc.Scores, math.NaN())
				tc.PassRates = append(tc.PassRates, math.NaN())
				tc.Statuses = append(tc.Statuses, models.StatusNA)
			}
		}
		tc.ScoreDelta = tc.Scores[n-1] - tc.Scores[0]
		tc.PassDelta = tc.PassRates[n-1] - tc.PassRates[0]
		report.TaskDeltas = append(report.TaskDeltas, tc)
	}

	return report
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

		fmt.Printf("  %-20s", "Total calls")
		for _, tm := range r.ToolMetrics {
			fmt.Printf("  %-9d", tm.TotalCalls)
		}
		fmt.Printf("  %+d\n", r.ToolMetrics[n-1].TotalCalls-r.ToolMetrics[0].TotalCalls)

		fmt.Printf("  %-20s", "Tasks w/ tools")
		for _, tm := range r.ToolMetrics {
			fmt.Printf("  %-9d", tm.TasksWithTools)
		}
		fmt.Printf("  %+d\n", r.ToolMetrics[n-1].TasksWithTools-r.ToolMetrics[0].TasksWithTools)

		fmt.Printf("  %-20s", "Avg calls/task")
		for _, tm := range r.ToolMetrics {
			fmt.Printf("  %-9.2f", tm.AvgCallsPerTask)
		}
		fmt.Printf("  %+.2f\n", r.ToolMetrics[n-1].AvgCallsPerTask-r.ToolMetrics[0].AvgCallsPerTask)

		fmt.Printf("  %-20s", "Success rate")
		for _, tm := range r.ToolMetrics {
			fmt.Printf("  %-9.1f%%", tm.SuccessRate*100)
		}
		fmt.Printf("  %+.1f%%\n", (r.ToolMetrics[n-1].SuccessRate-r.ToolMetrics[0].SuccessRate)*100)

		fmt.Printf("  %-20s", "Selection accuracy")
		for _, tm := range r.ToolMetrics {
			fmt.Printf("  %-9.1f%%", tm.SelectionAccuracy*100)
		}
		fmt.Printf("  %+.1f%%\n", (r.ToolMetrics[n-1].SelectionAccuracy-r.ToolMetrics[0].SelectionAccuracy)*100)

		for _, bucket := range []string{"0", "1", "2", "3+"} {
			fmt.Printf("  %-20s", "Tasks w/ "+bucket+" calls")
			for _, tm := range r.ToolMetrics {
				fmt.Printf("  %-9d", tm.CallCountHistogram[bucket])
			}
			fmt.Printf("  %+d\n",
				r.ToolMetrics[n-1].CallCountHistogram[bucket]-r.ToolMetrics[0].CallCountHistogram[bucket])
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
			if math.IsNaN(tc.Scores[i]) {
				fmt.Printf("  %-9s", "n/a")
			} else {
				fmt.Printf("  %-9.4f", tc.Scores[i])
			}
		}
		deltaIcon := " "
		if tc.ScoreDelta > 0 {
			deltaIcon = "↑"
		} else if tc.ScoreDelta < 0 {
			deltaIcon = "↓"
		}
		fmt.Printf("  %s%+.4f\n", deltaIcon, tc.ScoreDelta)
	}
	fmt.Println()
}

// hasAnyToolData reports whether any file has at least one recorded tool call.
// Suppresses the "TOOL USE" table for purely tool-free outcomes.
func hasAnyToolData(metrics []toolMetrics) bool {
	for _, m := range metrics {
		if m.TotalCalls > 0 {
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
