package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTaskOutcome builds a minimal TestOutcome for gate tests. Passing
// score=1.0 + status=Passed reflects a healthy task; score=0 + status=Failed
// reflects an outright failure.
func makeTaskOutcome(id, name string, golden bool, status models.Status, passRate, score float64) models.TestOutcome {
	return models.TestOutcome{
		TestID:      id,
		DisplayName: name,
		Golden:      golden,
		Status:      status,
		Stats: &models.TestStats{
			PassRate: passRate,
			AvgScore: score,
		},
	}
}

// makeOutcome builds a minimal EvaluationOutcome with the given tasks
// and an aggregate success rate.
func makeOutcome(t *testing.T, modelID string, successRate float64, tasks ...models.TestOutcome) *models.EvaluationOutcome {
	t.Helper()
	return &models.EvaluationOutcome{
		RunID:     "eval-gate",
		BenchName: "gate-test",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Setup: models.OutcomeSetup{
			ModelID:     modelID,
			RunsPerTest: 1,
			EngineType:  "mock",
		},
		Digest: models.OutcomeDigest{
			TotalTests:  len(tasks),
			SuccessRate: successRate,
		},
		TestOutcomes: tasks,
	}
}

// writeOutcome serializes an outcome to a temp file and returns the path.
func writeOutcome(t *testing.T, dir, name string, o *models.EvaluationOutcome) string {
	t.Helper()
	data, err := json.MarshalIndent(o, "", "  ")
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func TestGate_Pass_NoRegression(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 5,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyWarn,
		format:           gateFormatHuman,
	})
	assert.NoError(t, err)
	assert.Contains(t, stdout.String(), "PASS")
}

func TestGate_RegressionExceedsThreshold(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	// Drop 50pp (one of two tasks failed)
	curr := makeOutcome(t, "gpt-4o", 0.5,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusFailed, 0.0, 0.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 5,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatHuman,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitRegress, ge.code, "regression must exit 1")
}

func TestGate_RegressionUnderThreshold_Passes(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.00,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	// Tiny drop (1pp) — should pass when threshold is 5pp
	curr := makeOutcome(t, "gpt-4o", 0.99,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 0.99, 0.99),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 5,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatHuman,
	})
	assert.NoError(t, err, "1pp drop with 5pp threshold should pass")
}

func TestGate_GoldenFailure_HardFails(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden Task", true, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	// Aggregate pass rate still high (no threshold breach) but the golden
	// task failed — must exit 2.
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden Task", true, models.StatusFailed, 0.0, 0.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 50,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatJSON,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitGolden, ge.code, "golden failure must exit 2 even when aggregate is healthy")

	// JSON output should expose the golden failures list.
	var report gateReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	assert.Equal(t, "golden-fail", report.Outcome)
	assert.Contains(t, report.GoldenFailures, "task-golden")
}

func TestGate_GoldenFailure_Disabled_FallsThrough(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden Task", true, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden Task", true, models.StatusFailed, 0.0, 0.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 100,
		goldenMustPass:   false,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatJSON,
	})
	// With --golden-must-pass=false and a permissive aggregate threshold,
	// the gate should pass even though a golden task failed.
	assert.NoError(t, err)
}

func TestGate_NewTasksFail(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2-new", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 100,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyFail,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatJSON,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitRegress, ge.code)
}

func TestGate_RemovedTasksWarn_StillPasses(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	// Removed task-2 in current
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 100,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyWarn,
		format:           gateFormatJSON,
	})
	assert.NoError(t, err, "warn policy on removed tasks should not fail")

	var report gateReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	assert.Equal(t, "pass", report.Outcome)
	assert.Contains(t, report.RemovedTasks, "task-2")
	assert.NotEmpty(t, report.Warnings)
}

func TestGate_RemovedTasksFail(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 100,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyFail,
		format:           gateFormatJSON,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitRegress, ge.code)
}

func TestGate_MissingFile_ConfigError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     "/nonexistent/base.json",
		currentPath:      "/nonexistent/curr.json",
		maxRegressionPct: 5,
		format:           gateFormatHuman,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitConfErr, ge.code)
}

func TestGate_GoldenPriorityOverRegression(t *testing.T) {
	// When both a golden failure and an aggregate regression happen, the
	// exit code should be 2 (golden) — the more specific signal wins.
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden", true, models.StatusPassed, 1.0, 1.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 0.0,
		makeTaskOutcome("task-golden", "Golden", true, models.StatusFailed, 0.0, 0.0),
		makeTaskOutcome("task-2", "Task 2", false, models.StatusFailed, 0.0, 0.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 5,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatJSON,
	})
	require.Error(t, err)
	var ge *gateExitError
	require.True(t, errors.As(err, &ge))
	assert.Equal(t, GateExitGolden, ge.code, "golden failure must dominate over regression")
}

func TestGate_GithubActionsFormat_AnnotationsOnStderr(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-golden", "Golden", true, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 0.0,
		makeTaskOutcome("task-golden", "Golden", true, models.StatusFailed, 0.0, 0.0),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	summary := filepath.Join(dir, "summary.md")

	var stdout, stderr bytes.Buffer
	err := runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 100,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatGithubActions,
		summaryFile:      summary,
	})
	require.Error(t, err)
	assert.Contains(t, stderr.String(), "::error", "GHA format must emit ::error:: annotations on stderr")
	data, ferr := os.ReadFile(summary)
	require.NoError(t, ferr)
	assert.Contains(t, string(data), "# Waza Gate Report")
}

func TestGate_PolicyParsing(t *testing.T) {
	cases := []struct {
		in      string
		want    gatePolicy
		wantErr bool
	}{
		{"allow", gatePolicyAllow, false},
		{"WARN", gatePolicyWarn, false},
		{"  fail  ", gatePolicyFail, false},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := parseGatePolicy(c.in, "--flag")
		if c.wantErr {
			assert.Error(t, err, "input=%q", c.in)
		} else {
			require.NoError(t, err, "input=%q", c.in)
			assert.Equal(t, c.want, got)
		}
	}
}

func TestGate_FormatParsing(t *testing.T) {
	cases := []struct {
		in      string
		want    gateFormat
		wantErr bool
	}{
		{"human", gateFormatHuman, false},
		{"", gateFormatHuman, false},
		{"json", gateFormatJSON, false},
		{"MARKDOWN", gateFormatMarkdown, false},
		{"github-actions", gateFormatGithubActions, false},
		{"yaml", "", true},
	}
	for _, c := range cases {
		got, err := parseGateFormat(c.in)
		if c.wantErr {
			assert.Error(t, err, "input=%q", c.in)
		} else {
			require.NoError(t, err, "input=%q", c.in)
			assert.Equal(t, c.want, got)
		}
	}
}

func TestGate_MarkdownContainsKeyHeadings(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(t, "gpt-4o", 1.0,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusPassed, 1.0, 1.0),
	)
	curr := makeOutcome(t, "gpt-4o", 0.5,
		makeTaskOutcome("task-1", "Task 1", false, models.StatusFailed, 0.5, 0.5),
	)
	bp := writeOutcome(t, dir, "base.json", base)
	cp := writeOutcome(t, dir, "curr.json", curr)

	var stdout, stderr bytes.Buffer
	_ = runGate(&stdout, &stderr, &gateOpts{
		baselinePath:     bp,
		currentPath:      cp,
		maxRegressionPct: 5,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyAllow,
		format:           gateFormatMarkdown,
	})
	out := stdout.String()
	assert.True(t, strings.Contains(out, "# Waza Gate Report"))
	assert.True(t, strings.Contains(out, "Regressed Tasks"))
}

// TestGate_TestCaseGoldenYAML verifies the YAML tag for golden round-trips
// through the model. Documented for skill authors: `golden: true` in a task
// must surface on TestCase.Golden.
func TestGate_TestCaseGoldenYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "task.yaml")
	yaml := []byte(`id: t1
name: A
golden: true
inputs:
  prompt: hello
`)
	require.NoError(t, os.WriteFile(p, yaml, 0o644))

	tc, err := models.LoadTestCase(p)
	require.NoError(t, err)
	assert.True(t, tc.Golden)
}
