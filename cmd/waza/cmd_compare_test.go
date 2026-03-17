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

// createResultFile writes an EvalOutcome to a temp JSON file.
func createResultFile(t *testing.T, dir string, name string, outcome *models.EvalOutcome) string {
	t.Helper()
	data, err := json.MarshalIndent(outcome, "", "  ")
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func sampleOutcome(modelID string, score float64, successRate float64, taskScore float64) *models.EvalOutcome {
	return &models.EvalOutcome{
		RunID:       "eval-001",
		SkillTested: "test-skill",
		EvalName:    "test-eval",
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Setup: models.EvalSetup{
			RunsPerTest: 1,
			ModelID:     modelID,
			EngineType:  "mock",
			TimeoutSec:  30,
		},
		Digest: models.EvalDigest{
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
		TaskOutcomes: []models.TaskOutcome{
			{
				TestID:      "task-001",
				DisplayName: "Sample Task",
				Status:      models.StatusPassed,
				Stats: &models.TaskStats{
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
		[]*models.EvalOutcome{o1, o2},
	)

	assert.Len(t, report.Files, 2)
	assert.Equal(t, "gpt-4", report.Models[0])
	assert.Equal(t, "gpt-4o", report.Models[1])
	assert.InDelta(t, 0.15, report.AggScoreDelta, 0.001)
	assert.InDelta(t, 0.20, report.SuccessRDelta, 0.001)
	require.Len(t, report.TaskDeltas, 1)
	assert.InDelta(t, 0.15, report.TaskDeltas[0].ScoreDelta, 0.001)
}

func TestBuildComparisonReport_MissingTask(t *testing.T) {
	resetCompareGlobals()

	o1 := sampleOutcome("gpt-4", 0.80, 1.0, 0.80)
	o2 := sampleOutcome("gpt-4o", 0.90, 1.0, 0.90)
	// Add extra task only in o2
	o2.TaskOutcomes = append(o2.TaskOutcomes, models.TaskOutcome{
		TestID:      "task-002",
		DisplayName: "Extra Task",
		Status:      models.StatusPassed,
		Stats: &models.TaskStats{
			PassRate: 1.0,
			AvgScore: 0.90,
		},
	})

	report := buildComparisonReport(
		[]string{"r1.json", "r2.json"},
		[]*models.EvalOutcome{o1, o2},
	)

	require.Len(t, report.TaskDeltas, 2)
	// The extra task should show as n/a for file 1
	assert.Equal(t, "task-002", report.TaskDeltas[1].TaskID)
	assert.Equal(t, models.StatusNA, report.TaskDeltas[1].Statuses[0])
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
