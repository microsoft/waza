package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetCompareGlobals() {
	compareOutputFormat = "table"
}

// createResultFile writes an EvaluationOutcome to a temp JSON file.
func createResultFile(t *testing.T, dir string, name string, outcome *models.EvaluationOutcome) string {
	t.Helper()
	data, err := json.MarshalIndent(outcome, "", "  ")
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func sampleOutcome(modelID string, score float64, successRate float64, taskScore float64) *models.EvaluationOutcome {
	return &models.EvaluationOutcome{
		RunID:       "eval-001",
		SkillTested: "test-skill",
		BenchName:   "test-eval",
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Setup: models.OutcomeSetup{
			RunsPerTest: 1,
			ModelID:     modelID,
			EngineType:  "mock",
			TimeoutSec:  30,
		},
		Digest: models.OutcomeDigest{
			TotalTests:     1,
			Succeeded:      1,
			Failed:         0,
			Errors:         0,
			SuccessRate:    successRate,
			AggregateScore: score,
			MinScore:       score,
			MaxScore:       score,
			DurationMs:     1000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "task-001",
				DisplayName: "Sample Task",
				Status:      models.StatusPassed,
				Stats: &models.TestStats{
					PassRate: successRate,
					AvgScore: taskScore,
					MinScore: taskScore,
					MaxScore: taskScore,
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Argument validation
// ---------------------------------------------------------------------------

func TestCompareCommand_RequiresAtLeastTwoArgs(t *testing.T) {
	resetCompareGlobals()

	tests := []struct {
		name string
		args []string
	}{
		{"no args", []string{}},
		{"one arg", []string{"one.json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newCompareCommand()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			assert.Error(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

func TestCompareCommand_MissingFile(t *testing.T) {
	resetCompareGlobals()

	cmd := newCompareCommand()
	cmd.SetArgs([]string{"nonexistent1.json", "nonexistent2.json"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load")
}

func TestCompareCommand_InvalidJSON(t *testing.T) {
	resetCompareGlobals()

	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{invalid"), 0o644))

	good := createResultFile(t, dir, "good.json", sampleOutcome("gpt-4", 0.8, 1.0, 0.8))

	cmd := newCompareCommand()
	cmd.SetArgs([]string{good, bad})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load")
}

func TestCompareCommand_InvalidFormat(t *testing.T) {
	resetCompareGlobals()

	dir := t.TempDir()
	f1 := createResultFile(t, dir, "r1.json", sampleOutcome("gpt-4", 0.8, 1.0, 0.8))
	f2 := createResultFile(t, dir, "r2.json", sampleOutcome("gpt-4", 0.9, 1.0, 0.9))

	cmd := newCompareCommand()
	cmd.SetArgs([]string{f1, f2, "--format", "xml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported format")
}

// ---------------------------------------------------------------------------
// Table output
// ---------------------------------------------------------------------------

func TestCompareCommand_TableOutput(t *testing.T) {
	resetCompareGlobals()

	dir := t.TempDir()
	f1 := createResultFile(t, dir, "r1.json", sampleOutcome("gpt-4", 0.80, 1.0, 0.80))
	f2 := createResultFile(t, dir, "r2.json", sampleOutcome("gpt-4o", 0.95, 1.0, 0.95))

	cmd := newCompareCommand()
	cmd.SetArgs([]string{f1, f2})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// JSON output
// ---------------------------------------------------------------------------

func TestCompareCommand_JSONOutput(t *testing.T) {
	resetCompareGlobals()

	dir := t.TempDir()
	f1 := createResultFile(t, dir, "r1.json", sampleOutcome("gpt-4", 0.80, 1.0, 0.80))
	f2 := createResultFile(t, dir, "r2.json", sampleOutcome("gpt-4o", 0.95, 1.0, 0.95))

	cmd := newCompareCommand()
	cmd.SetArgs([]string{f1, f2, "--format", "json"})

	err := cmd.Execute()
	assert.NoError(t, err)
}

func TestCompareCommand_LegacyJSONPayloadAndProcessExits(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	for _, tc := range []struct {
		name string
		from float64
		to   float64
	}{{"improvement", 0.25, 0.75}, {"regression", 0.75, 0.25}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			baseline := sampleOutcome("baseline", tc.from, tc.from, tc.from)
			current := sampleOutcome("current", tc.to, tc.to, tc.to)
			extra := baseline.TestOutcomes[0]
			extra.TestID, extra.DisplayName = "task-002", "Second Task"
			baseline.TestOutcomes = append(baseline.TestOutcomes, extra)
			extra = current.TestOutcomes[0]
			extra.TestID, extra.DisplayName = "task-002", "Second Task"
			current.TestOutcomes = append([]models.TestOutcome{extra}, current.TestOutcomes...)
			baseline.Digest.TotalTests, current.Digest.TotalTests = 2, 2
			from := createResultFile(t, dir, "baseline.json", baseline)
			to := createResultFile(t, dir, "current.json", current)
			output, code := runOfflineComparisonCLI(t, binary, "compare", from, to, "--format", "json")
			require.Equal(t, 0, code, "%s", output)
			var payload map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(output, &payload), "%s", output)
			require.ElementsMatch(t, []string{
				"files", "models", "aggregate_scores", "success_rates", "aggregate_score_delta",
				"success_rate_delta", "task_deltas", "total_tests", "durations_ms",
				"duration_delta_ms", "tool_metrics",
			}, func() []string {
				keys := make([]string, 0, len(payload))
				for key := range payload {
					keys = append(keys, key)
				}
				return keys
			}(), "legacy JSON must not acquire release-policy fields")
			var report comparisonReport
			require.NoError(t, json.Unmarshal(output, &report))
			require.Equal(t, []string{from, to}, report.Files)
			require.Equal(t, []string{"baseline", "current"}, report.Models)
			require.Equal(t, []float64{tc.from, tc.to}, report.AggScores)
			require.Equal(t, []float64{tc.from, tc.to}, report.SuccessRates)
			require.Equal(t, tc.to-tc.from, report.AggScoreDelta)
			require.Equal(t, tc.to-tc.from, report.SuccessRDelta)
			require.Equal(t, []int{2, 2}, report.TotalTests)
			require.Equal(t, []int64{1000, 1000}, report.DurationsMs)
			require.Zero(t, report.DurationDeltaM)
			require.Len(t, report.TaskDeltas, 2)
			for i, id := range []string{"task-001", "task-002"} {
				task := report.TaskDeltas[i]
				require.Equal(t, id, task.TaskID)
				require.Equal(t, floatPtrs(tc.from, tc.to), task.Scores)
				require.Equal(t, floatPtrs(tc.from, tc.to), task.PassRates)
				require.Equal(t, []models.Status{models.StatusPassed, models.StatusPassed}, task.Statuses)
				require.Equal(t, floatPtrs(tc.to - tc.from)[0], task.ScoreDelta)
				require.Equal(t, floatPtrs(tc.to - tc.from)[0], task.PassDelta)
				require.Nil(t, task.Availability)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Three-way compare
// ---------------------------------------------------------------------------

func TestCompareCommand_ThreeFiles(t *testing.T) {
	resetCompareGlobals()

	dir := t.TempDir()
	f1 := createResultFile(t, dir, "r1.json", sampleOutcome("gpt-4", 0.70, 0.8, 0.70))
	f2 := createResultFile(t, dir, "r2.json", sampleOutcome("gpt-4o", 0.85, 0.9, 0.85))
	f3 := createResultFile(t, dir, "r3.json", sampleOutcome("gpt-4.1", 0.95, 1.0, 0.95))

	cmd := newCompareCommand()
	cmd.SetArgs([]string{f1, f2, f3})

	err := cmd.Execute()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Report building logic
// ---------------------------------------------------------------------------

func TestBuildComparisonReport_Deltas(t *testing.T) {
	resetCompareGlobals()

	o1 := sampleOutcome("gpt-4", 0.80, 0.80, 0.80)
	o2 := sampleOutcome("gpt-4o", 0.95, 1.00, 0.95)

	report := buildComparisonReport(
		[]string{"r1.json", "r2.json"},
		[]*models.EvaluationOutcome{o1, o2},
	)

	assert.Len(t, report.Files, 2)
	assert.Equal(t, "gpt-4", report.Models[0])
	assert.Equal(t, "gpt-4o", report.Models[1])
	assert.InDelta(t, 0.15, report.AggScoreDelta, 0.001)
	assert.InDelta(t, 0.20, report.SuccessRDelta, 0.001)
	require.Len(t, report.TaskDeltas, 1)
	require.NotNil(t, report.TaskDeltas[0].ScoreDelta)
	assert.InDelta(t, 0.15, *report.TaskDeltas[0].ScoreDelta, 0.001)
}

func TestBuildComparisonReport_MissingTask(t *testing.T) {
	resetCompareGlobals()

	o1 := sampleOutcome("gpt-4", 0.80, 1.0, 0.80)
	o2 := sampleOutcome("gpt-4o", 0.90, 1.0, 0.90)
	// Add extra task only in o2
	o2.TestOutcomes = append(o2.TestOutcomes, models.TestOutcome{
		TestID:      "task-002",
		DisplayName: "Extra Task",
		Status:      models.StatusPassed,
		Stats: &models.TestStats{
			PassRate: 1.0,
			AvgScore: 0.90,
		},
	})

	report := buildComparisonReport(
		[]string{"r1.json", "r2.json"},
		[]*models.EvaluationOutcome{o1, o2},
	)

	require.Len(t, report.TaskDeltas, 2)
	// The extra task should show as n/a for file 1
	assert.Equal(t, "task-002", report.TaskDeltas[1].TaskID)
	assert.Equal(t, models.StatusNA, report.TaskDeltas[1].Statuses[0])
	assert.Nil(t, report.TaskDeltas[1].Scores[0])
	assert.Nil(t, report.TaskDeltas[1].ScoreDelta)
	assert.Equal(t, []string{availabilityTaskMissing, availabilityObserved}, report.TaskDeltas[1].Availability)
	require.NotPanics(t, func() { printComparisonTable(report) })
}

func TestBuildComparisonReport_HistoricalToolEvidenceTable(t *testing.T) {
	historical := sampleOutcome("old", 0.8, 1.0, 0.8)
	historical.SchemaVersion = "1.0"
	current := sampleOutcome("new", 0.8, 1.0, 0.8)
	current.TestOutcomes[0].Runs = []models.RunResult{{ToolEvents: []models.ToolEvent{{ToolName: "bash", Success: true}}}}

	report := buildComparisonReport([]string{"r1.json", "r2.json"}, []*models.EvaluationOutcome{historical, current})

	require.Nil(t, report.ToolMetrics[0])
	require.NotNil(t, report.ToolMetrics[1])
	require.Equal(t, 1, report.ToolMetrics[1].TotalCalls)
	require.NotPanics(t, func() { printComparisonTable(report) })
}

func floatPtrs(values ...float64) []*float64 {
	ptrs := make([]*float64, len(values))
	for i := range values {
		ptrs[i] = &values[i]
	}
	return ptrs
}

func TestCompareCommand_ChangingTaskSets(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	withTask := func(o *models.EvaluationOutcome, id string, stats *models.TestStats) *models.EvaluationOutcome {
		o.TestOutcomes = append(o.TestOutcomes, models.TestOutcome{
			TestID: id, DisplayName: id, Status: models.StatusFailed, Stats: stats,
		})
		return o
	}
	type taskJSON struct {
		TaskID       string     `json:"task_id"`
		Scores       []*float64 `json:"scores"`
		PassRates    []*float64 `json:"pass_rates"`
		ScoreDelta   *float64   `json:"score_delta"`
		PassDelta    *float64   `json:"pass_rate_delta"`
		Availability []string   `json:"availability"`
	}
	type reportJSON struct {
		TaskDeltas              []taskJSON        `json:"task_deltas"`
		ToolMetrics             []json.RawMessage `json:"tool_metrics"`
		ToolMetricsAvailability []string          `json:"tool_metrics_availability"`
	}
	run := func(t *testing.T, baseline, current *models.EvaluationOutcome) (reportJSON, map[string]json.RawMessage) {
		t.Helper()
		dir := t.TempDir()
		from := createResultFile(t, dir, "baseline.json", baseline)
		to := createResultFile(t, dir, "current.json", current)
		output, code := runOfflineComparisonCLI(t, binary, "compare", from, to, "--format", "json")
		require.Equal(t, 0, code, "%s", output)
		require.True(t, json.Valid(output), "%s", output)
		var report reportJSON
		require.NoError(t, json.Unmarshal(output, &report))
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(output, &raw))
		return report, raw
	}
	byID := func(r reportJSON) map[string]taskJSON {
		tasks := make(map[string]taskJSON, len(r.TaskDeltas))
		for _, task := range r.TaskDeltas {
			tasks[task.TaskID] = task
		}
		return tasks
	}

	t.Run("disjoint", func(t *testing.T) {
		baseline := sampleOutcome("baseline", 0.8, 1.0, 0.8)
		current := sampleOutcome("current", 0.5, 1.0, 0.5)
		current.TestOutcomes[0].TestID = "task-B"
		report, _ := run(t, baseline, current)
		tasks := byID(report)
		require.Len(t, tasks, 2)
		a, b := tasks["task-001"], tasks["task-B"]
		require.Equal(t, floatPtrs(0.8), a.Scores[:1])
		require.Nil(t, a.Scores[1])
		require.Nil(t, a.PassRates[1])
		require.Nil(t, a.ScoreDelta)
		require.Nil(t, a.PassDelta)
		require.Equal(t, []string{availabilityObserved, availabilityTaskMissing}, a.Availability)
		require.Nil(t, b.Scores[0])
		require.Nil(t, b.ScoreDelta)
		require.Equal(t, []string{availabilityTaskMissing, availabilityObserved}, b.Availability)
	})

	t.Run("overlapping with observed zero and missing stats", func(t *testing.T) {
		baseline := withTask(sampleOutcome("baseline", 0.8, 1.0, 0.8), "zero", &models.TestStats{})
		baseline = withTask(baseline, "no-stats", nil)
		current := withTask(sampleOutcome("current", 0.9, 1.0, 0.9), "zero", &models.TestStats{AvgScore: 0.25, PassRate: 0.5})
		current = withTask(current, "no-stats", &models.TestStats{AvgScore: 0.4, PassRate: 1})
		current = withTask(current, "new-only", &models.TestStats{})
		report, _ := run(t, baseline, current)
		tasks := byID(report)
		require.Len(t, tasks, 4)

		shared := tasks["task-001"]
		require.NotNil(t, shared.ScoreDelta)
		require.InDelta(t, 0.1, *shared.ScoreDelta, 1e-9)
		require.Nil(t, shared.Availability, "fully observed tasks keep the legacy shape")

		zero := tasks["zero"]
		require.Equal(t, floatPtrs(0, 0.25), zero.Scores)
		require.Equal(t, floatPtrs(0, 0.5), zero.PassRates)
		require.Equal(t, floatPtrs(0.25)[0], zero.ScoreDelta)
		require.Equal(t, floatPtrs(0.5)[0], zero.PassDelta)
		require.Nil(t, zero.Availability)

		noStats := tasks["no-stats"]
		require.Nil(t, noStats.Scores[0])
		require.Nil(t, noStats.ScoreDelta)
		require.Nil(t, noStats.PassDelta)
		require.Equal(t, []string{availabilityStatsMissing, availabilityObserved}, noStats.Availability)

		newOnly := tasks["new-only"]
		require.Equal(t, floatPtrs(0)[0], newOnly.Scores[1], "observed zero is not null")
		require.Nil(t, newOnly.ScoreDelta)
		require.Equal(t, []string{availabilityTaskMissing, availabilityObserved}, newOnly.Availability)
	})

	t.Run("historical tool evidence", func(t *testing.T) {
		baseline := sampleOutcome("baseline", 0.8, 1.0, 0.8)
		baseline.SchemaVersion = "1.0"
		current := sampleOutcome("current", 0.8, 1.0, 0.8)
		report, _ := run(t, baseline, current)
		require.Len(t, report.ToolMetrics, 2)
		require.JSONEq(t, "null", string(report.ToolMetrics[0]))
		var observed toolMetrics
		require.NoError(t, json.Unmarshal(report.ToolMetrics[1], &observed))
		require.Zero(t, observed.TotalCalls)
		require.Equal(t, []string{availabilityToolEventsMissing, availabilityObserved}, report.ToolMetricsAvailability)

		_, raw := run(t, sampleOutcome("baseline", 0.8, 1.0, 0.8), current)
		_, present := raw["tool_metrics_availability"]
		require.False(t, present, "current results keep the legacy shape")
	})

	t.Run("table output marks unavailable values", func(t *testing.T) {
		baseline := withTask(sampleOutcome("baseline", 0.8, 1.0, 0.8), "zero", &models.TestStats{})
		baseline = withTask(baseline, "no-stats", nil)
		baseline.SchemaVersion = "1.0"
		current := withTask(sampleOutcome("current", 0.9, 1.0, 0.9), "zero", &models.TestStats{AvgScore: 0.25})
		current = withTask(current, "no-stats", &models.TestStats{AvgScore: 0.4})
		current = withTask(current, "new-only", &models.TestStats{})
		current.TestOutcomes[0].Runs = []models.RunResult{{ToolEvents: []models.ToolEvent{{ToolName: "bash", Success: true}}}}
		dir := t.TempDir()
		from := createResultFile(t, dir, "baseline.json", baseline)
		to := createResultFile(t, dir, "current.json", current)
		output, code := runOfflineComparisonCLI(t, binary, "compare", from, to)
		require.Equal(t, 0, code, "%s", output)
		text := string(output)
		for _, pattern := range []string{
			`(?m)^\s+Total calls\s+n/a\s+1\s+n/a$`,
			`(?m)^\s+Tasks w/ 0 calls\s+n/a\s+3\s+n/a$`,
			`(?m)^\s+Sample Task\s+0\.8000\s+0\.9000\s+↑\+0\.1000$`,
			`(?m)^\s+zero\s+0\.0000\s+0\.2500\s+↑\+0\.2500$`,
			`(?m)^\s+no-stats\s+n/a\s+0\.4000\s+n/a$`,
			`(?m)^\s+new-only\s+n/a\s+0\.0000\s+n/a$`,
		} {
			require.Regexp(t, pattern, text)
		}
	})
}

// ---------------------------------------------------------------------------
// Root command wiring
// ---------------------------------------------------------------------------

func TestRootCommand_HasCompareSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "compare" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'compare' subcommand")
}

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

func TestCompareCommand_FormatFlagParsed(t *testing.T) {
	cmd := newCompareCommand()
	require.NoError(t, cmd.ParseFlags([]string{"--format", "json"}))

	val, err := cmd.Flags().GetString("format")
	require.NoError(t, err)
	assert.Equal(t, "json", val)
}

func TestCompareCommand_ShortFormatFlag(t *testing.T) {
	cmd := newCompareCommand()
	require.NoError(t, cmd.ParseFlags([]string{"-f", "json"}))

	val, err := cmd.Flags().GetString("format")
	require.NoError(t, err)
	assert.Equal(t, "json", val)
}

// ---------------------------------------------------------------------------
// Tool-use aggregate metrics (issue #366).
// ---------------------------------------------------------------------------

func TestComputeToolMetrics_NoToolData(t *testing.T) {
	o := sampleOutcome("none", 0.5, 0.5, 0.5)
	tm := computeToolMetrics(o)
	require.Equal(t, 0, tm.TotalCalls)
	require.Equal(t, 0, tm.TasksWithTools)
	require.Equal(t, 0.0, tm.AvgCallsPerTask)
	require.Equal(t, 0.0, tm.SuccessRate)
	require.Equal(t, 0.0, tm.SelectionAccuracy)
	// histogram map is present; with no runs, no buckets are incremented
	require.NotNil(t, tm.CallCountHistogram)
	require.Equal(t, 0, tm.CallCountHistogram["1"])
	require.Equal(t, 0, tm.CallCountHistogram["2"])
	require.Equal(t, 0, tm.CallCountHistogram["3+"])
}

func TestComputeToolMetrics_TotalsAndSuccessRate(t *testing.T) {
	o := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{
				Runs: []models.RunResult{{
					ToolEvents: []models.ToolEvent{
						{ToolName: "bash", Success: true},
						{ToolName: "view", Success: true},
						{ToolName: "bash", Success: false},
					},
				}},
			},
			{
				Runs: []models.RunResult{{
					ToolEvents: []models.ToolEvent{
						{ToolName: "edit", Success: true},
					},
				}},
			},
		},
	}
	tm := computeToolMetrics(o)
	require.Equal(t, 4, tm.TotalCalls)
	require.Equal(t, 2, tm.TasksWithTools)
	require.InDelta(t, 2.0, tm.AvgCallsPerTask, 1e-9)
	require.InDelta(t, 0.75, tm.SuccessRate, 1e-9)
}

func TestComputeToolMetrics_HistogramBuckets(t *testing.T) {
	mkTask := func(n int) models.TestOutcome {
		evs := make([]models.ToolEvent, n)
		for i := range evs {
			evs[i] = models.ToolEvent{ToolName: "bash", Success: true}
		}
		return models.TestOutcome{Runs: []models.RunResult{{ToolEvents: evs}}}
	}
	o := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			mkTask(0), mkTask(1), mkTask(2), mkTask(5),
		},
	}
	tm := computeToolMetrics(o)
	require.Equal(t, 1, tm.CallCountHistogram["0"])
	require.Equal(t, 1, tm.CallCountHistogram["1"])
	require.Equal(t, 1, tm.CallCountHistogram["2"])
	require.Equal(t, 1, tm.CallCountHistogram["3+"])
}

func TestComputeToolMetrics_SelectionAccuracy(t *testing.T) {
	o := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{Runs: []models.RunResult{{
				ToolEvents: []models.ToolEvent{{ToolName: "bash", Success: true}},
				Validations: map[string]models.GraderResults{
					"calls": {Type: models.GraderKindToolCalls, Passed: true},
				},
			}}},
			{Runs: []models.RunResult{{
				ToolEvents: []models.ToolEvent{{ToolName: "bash", Success: true}},
				Validations: map[string]models.GraderResults{
					"calls": {Type: models.GraderKindToolCalls, Passed: false},
				},
			}}},
			// Task without a tool_calls grader is excluded from denominator.
			{Runs: []models.RunResult{{
				ToolEvents: []models.ToolEvent{{ToolName: "bash", Success: true}},
			}}},
		},
	}
	tm := computeToolMetrics(o)
	require.InDelta(t, 0.5, tm.SelectionAccuracy, 1e-9)
}

func TestHasAnyToolData(t *testing.T) {
	require.False(t, hasAnyToolData([]*toolMetrics{{}, {}}))
	require.False(t, hasAnyToolData([]*toolMetrics{nil, {}}))
	require.True(t, hasAnyToolData([]*toolMetrics{nil, {TotalCalls: 3}}))
}

// TestComputeToolMetrics_HistogramPerTaskAcrossTrials verifies the histogram
// buckets per-task (summing across all trials), not per-run. With trials > 1,
// a task that called one tool per trial should land in a single bucket
// reflecting the total, never increment multiple buckets.
func TestComputeToolMetrics_HistogramPerTaskAcrossTrials(t *testing.T) {
	mkEvent := func() models.ToolEvent {
		return models.ToolEvent{ToolName: "bash", Success: true}
	}
	o := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			// Task with 3 trials × 1 call each → 3 calls total → bucket "3+".
			{Runs: []models.RunResult{
				{ToolEvents: []models.ToolEvent{mkEvent()}},
				{ToolEvents: []models.ToolEvent{mkEvent()}},
				{ToolEvents: []models.ToolEvent{mkEvent()}},
			}},
			// Task with 2 trials, one empty + one with 1 call → 1 total → "1".
			{Runs: []models.RunResult{
				{ToolEvents: nil},
				{ToolEvents: []models.ToolEvent{mkEvent()}},
			}},
			// Task with 2 trials × 1 call each → 2 total → "2".
			{Runs: []models.RunResult{
				{ToolEvents: []models.ToolEvent{mkEvent()}},
				{ToolEvents: []models.ToolEvent{mkEvent()}},
			}},
		},
	}
	tm := computeToolMetrics(o)
	require.Equal(t, 0, tm.CallCountHistogram["0"])
	require.Equal(t, 1, tm.CallCountHistogram["1"])
	require.Equal(t, 1, tm.CallCountHistogram["2"])
	require.Equal(t, 1, tm.CallCountHistogram["3+"])
	// Total across all trials of all tasks: 3 + 1 + 2 = 6.
	require.Equal(t, 6, tm.TotalCalls)
	// Three tasks made tool calls.
	require.Equal(t, 3, tm.TasksWithTools)
}
