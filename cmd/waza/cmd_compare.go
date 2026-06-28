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

func printComparisonJSON(r *comparisonReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal comparison report: %w", err)
	}
	fmt.Println(string(data))
	return nil
}
