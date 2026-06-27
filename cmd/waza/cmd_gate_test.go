package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executeGateCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newGateCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func requireExitCode(t *testing.T, err error, want int) {
	t.Helper()
	require.Error(t, err)
	var exitErr exitCoder
	require.True(t, errors.As(err, &exitErr), "expected exitCoder, got %T", err)
	assert.Equal(t, want, exitErr.ExitCode())
}

func TestGateCommand_RegressionThresholdExitCode(t *testing.T) {
	dir := t.TempDir()
	baseline := createResultFile(t, dir, "baseline.json", sampleOutcome("gpt-4o", 1.0, 1.0, 1.0))
	current := createResultFile(t, dir, "current.json", sampleOutcome("gpt-4o", 0.9, 0.9, 0.9))

	out, err := executeGateCommand(t,
		"--baseline", baseline,
		"--current", current,
		"--max-regression-pct", "5",
	)

	requireExitCode(t, err, ExitGateRegression)
	assert.Contains(t, out, "Waza gate: FAIL")
	assert.Contains(t, out, "10.00 percentage points")
}

func TestGateCommand_GoldenFailureExitCode(t *testing.T) {
	dir := t.TempDir()
	baselineOutcome := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)
	currentOutcome := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)
	currentOutcome.TestOutcomes[0].Golden = true
	currentOutcome.TestOutcomes[0].Status = models.StatusFailed
	currentOutcome.TestOutcomes[0].Stats.PassRate = 0

	baseline := createResultFile(t, dir, "baseline.json", baselineOutcome)
	current := createResultFile(t, dir, "current.json", currentOutcome)

	out, err := executeGateCommand(t,
		"--baseline", baseline,
		"--current", current,
		"--golden-must-pass",
	)

	requireExitCode(t, err, ExitGateGoldenFailure)
	assert.Contains(t, out, "Waza gate: GOLDEN-FAILURE")
	assert.Contains(t, out, "golden task")
}

func TestGateCommand_TaskSetPolicies(t *testing.T) {
	baseline := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)
	current := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)
	current.TestOutcomes = append(current.TestOutcomes, models.TestOutcome{
		TestID:      "task-new",
		DisplayName: "New Task",
		Status:      models.StatusPassed,
		Stats:       &models.TestStats{PassRate: 1, AvgScore: 1},
	})

	report := buildGateReport(gateOptions{
		maxRegressionPct: 0,
		onNewTasks:       string(gateTaskDeltaFail),
		onRemovedTasks:   string(gateTaskDeltaAllow),
	}, baseline, current)

	require.False(t, report.Passed)
	assert.Equal(t, ExitGateRegression, report.ExitCode)
	require.Len(t, report.NewTasks, 1)
	require.Len(t, report.Failures, 1)
	assert.Equal(t, "new_task", report.Failures[0].Kind)
}

func TestGateCommand_RemovedTaskWarnDoesNotFail(t *testing.T) {
	baseline := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)
	baseline.TestOutcomes = append(baseline.TestOutcomes, models.TestOutcome{
		TestID:      "task-removed",
		DisplayName: "Removed Task",
		Status:      models.StatusPassed,
		Stats:       &models.TestStats{PassRate: 1, AvgScore: 1},
	})
	current := sampleOutcome("gpt-4o", 1.0, 1.0, 1.0)

	report := buildGateReport(gateOptions{
		maxRegressionPct: 0,
		onNewTasks:       string(gateTaskDeltaAllow),
		onRemovedTasks:   string(gateTaskDeltaWarn),
	}, baseline, current)

	require.True(t, report.Passed)
	require.Len(t, report.RemovedTasks, 1)
	require.Len(t, report.Warnings, 1)
	assert.Equal(t, "removed_task", report.Warnings[0].Kind)
}

func TestGateCommand_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	baseline := createResultFile(t, dir, "baseline.json", sampleOutcome("gpt-4o", 1.0, 1.0, 1.0))
	current := createResultFile(t, dir, "current.json", sampleOutcome("gpt-4o", 1.0, 1.0, 1.0))

	out, err := executeGateCommand(t,
		"--baseline", baseline,
		"--current", current,
		"--format", "json",
	)

	require.NoError(t, err)
	var report gateReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.True(t, report.Passed)
	assert.Equal(t, ExitSuccess, report.ExitCode)
}

func TestGateCommand_GitHubActionsWritesAnnotationsAndSummary(t *testing.T) {
	dir := t.TempDir()
	summaryPath := filepath.Join(dir, "summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryPath)

	baseline := createResultFile(t, dir, "baseline.json", sampleOutcome("gpt-4o", 1.0, 1.0, 1.0))
	current := createResultFile(t, dir, "current.json", sampleOutcome("gpt-4o", 0.8, 0.8, 0.8))

	out, err := executeGateCommand(t,
		"--baseline", baseline,
		"--current", current,
		"--max-regression-pct", "5",
		"--format", "github-actions",
	)

	requireExitCode(t, err, ExitGateRegression)
	assert.Contains(t, out, "::error title=Waza gate::")
	summary, readErr := os.ReadFile(summaryPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(summary), "## Waza gate: FAIL")
}

func TestGateCommand_ConfigErrorsUseExitCode3(t *testing.T) {
	_, err := executeGateCommand(t,
		"--baseline", "baseline.json",
		"--current", "current.json",
		"--on-new-tasks", "block",
	)

	requireExitCode(t, err, ExitGateConfigError)
}

func TestRootCommand_HasGateSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "gate" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'gate' subcommand")
}
