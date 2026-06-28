package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

// makeOutcome builds a minimal EvaluationOutcome with the given per-task
// statuses and an explicit success rate, suitable for gate-level tests.
func makeOutcome(successRate float64, tasks []models.TestOutcome) *models.EvaluationOutcome {
	o := &models.EvaluationOutcome{
		TestOutcomes: tasks,
	}
	o.Digest.SuccessRate = successRate
	o.Digest.TotalTests = len(tasks)
	for _, t := range tasks {
		switch t.Status {
		case models.StatusPassed:
			o.Digest.Succeeded++
		case models.StatusFailed:
			o.Digest.Failed++
		}
	}
	return o
}

func writeOutcomeFile(t *testing.T, dir, name string, o *models.EvaluationOutcome) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func defaultOpts(baseline, current string) *gateOptions {
	return &gateOptions{
		baselinePath:     baseline,
		currentPath:      current,
		maxRegressionPct: 0,
		goldenMustPass:   true,
		onNewTasks:       gatePolicyAllow,
		onRemovedTasks:   gatePolicyWarn,
		format:           gateFormatJSON,
	}
}

func TestGate_PassWhenNoRegression(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "t2", Status: models.StatusPassed},
	})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "t2", Status: models.StatusPassed},
	})
	bp := writeOutcomeFile(t, dir, "baseline.json", base)
	cp := writeOutcomeFile(t, dir, "current.json", curr)

	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	var r GateReport
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ExitCode != GateExitPass {
		t.Errorf("expected exit %d, got %d (outcome=%s)", GateExitPass, r.ExitCode, r.Outcome)
	}
}

func TestGate_RegressionExceedsThreshold(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.95, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.80, []models.TestOutcome{{TestID: "t1", Status: models.StatusFailed}})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	if err == nil {
		t.Fatalf("expected non-nil error")
	}
	var ec *ExitCodeError
	if !errors.As(err, &ec) {
		t.Fatalf("expected ExitCodeError, got %T", err)
	}
	if ec.Code != GateExitRegression {
		t.Errorf("expected exit %d, got %d", GateExitRegression, ec.Code)
	}
}

func TestGate_RegressionWithinThresholdPasses(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.95, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.92, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	// Default --max-regression-pct is 0, so we must explicitly allow the 3pp drop.
	opts := defaultOpts(bp, cp)
	opts.maxRegressionPct = 5.0

	var buf bytes.Buffer
	if err := runGate(&buf, opts); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestGate_DefaultZeroThresholdFailsAnyRegression(t *testing.T) {
	// With the default --max-regression-pct=0, even a 3pp drop in pass rate
	// must trigger a regression exit (code 1).
	dir := t.TempDir()
	base := makeOutcome(0.95, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.92, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	var ec *ExitCodeError
	if !errors.As(err, &ec) {
		t.Fatalf("expected ExitCodeError for regression, got %v", err)
	}
	if ec.Code != GateExitRegression {
		t.Errorf("expected exit %d, got %d", GateExitRegression, ec.Code)
	}
}

func TestGate_GoldenFailureTakesPrecedenceOverRegression(t *testing.T) {
	dir := t.TempDir()
	// Big regression AND a golden failure. Golden must win (exit 2).
	base := makeOutcome(0.95, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusPassed},
	})
	curr := makeOutcome(0.50, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusFailed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	var ec *ExitCodeError
	if !errors.As(err, &ec) {
		t.Fatalf("expected ExitCodeError, got %v", err)
	}
	if ec.Code != GateExitGoldenFailure {
		t.Errorf("expected exit %d (golden), got %d", GateExitGoldenFailure, ec.Code)
	}
}

func TestGate_GoldenMustPassFalseAllowsGoldenFailure(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusPassed},
	})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusFailed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.goldenMustPass = false
	var buf bytes.Buffer
	if err := runGate(&buf, opts); err != nil {
		t.Fatalf("expected pass (golden disabled), got %v", err)
	}
}

func TestGate_GoldenDetectedFromBaselineEvenIfMissingInCurrent(t *testing.T) {
	// If the baseline declares a task golden but the current results.json
	// was produced from an older YAML missing the flag, the gate should
	// still enforce it. This is the "conservative once-golden-always-golden"
	// behavior documented in cmd_gate.go.
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusPassed},
	})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: false, Status: models.StatusFailed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	var ec *ExitCodeError
	if !errors.As(err, &ec) || ec.Code != GateExitGoldenFailure {
		t.Fatalf("expected golden failure exit %d, got %v", GateExitGoldenFailure, err)
	}
}

func TestGate_NewTasksPolicyFailFails(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "t2", Status: models.StatusPassed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.onNewTasks = gatePolicyFail
	var buf bytes.Buffer
	err := runGate(&buf, opts)
	var ec *ExitCodeError
	if !errors.As(err, &ec) || ec.Code != GateExitRegression {
		t.Fatalf("expected regression exit %d for new-task fail policy, got %v", GateExitRegression, err)
	}

	var r GateReport
	_ = json.Unmarshal(buf.Bytes(), &r)
	if len(r.NewTasks) != 1 || r.NewTasks[0] != "t2" {
		t.Errorf("expected new task t2, got %v", r.NewTasks)
	}
}

func TestGate_NewTasksPolicyWarn(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "t2", Status: models.StatusPassed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.onNewTasks = gatePolicyWarn
	var buf bytes.Buffer
	if err := runGate(&buf, opts); err != nil {
		t.Fatalf("expected pass with warn, got %v", err)
	}
	var r GateReport
	_ = json.Unmarshal(buf.Bytes(), &r)
	if len(r.Warnings) == 0 {
		t.Errorf("expected at least one warning, got none")
	}
}

func TestGate_RemovedTasksPolicyFail(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "t2", Status: models.StatusPassed},
	})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.onRemovedTasks = gatePolicyFail
	var buf bytes.Buffer
	err := runGate(&buf, opts)
	var ec *ExitCodeError
	if !errors.As(err, &ec) || ec.Code != GateExitRegression {
		t.Fatalf("expected regression exit, got %v", err)
	}
}

func TestGate_ConfigErrors(t *testing.T) {
	cases := map[string]*gateOptions{
		"missing baseline":  {currentPath: "x.json", format: gateFormatHuman, onNewTasks: "allow", onRemovedTasks: "warn"},
		"missing current":   {baselinePath: "x.json", format: gateFormatHuman, onNewTasks: "allow", onRemovedTasks: "warn"},
		"bad new policy":    {baselinePath: "a", currentPath: "b", format: gateFormatHuman, onNewTasks: "bogus", onRemovedTasks: "warn"},
		"bad remove policy": {baselinePath: "a", currentPath: "b", format: gateFormatHuman, onNewTasks: "allow", onRemovedTasks: "bogus"},
		"bad format":        {baselinePath: "a", currentPath: "b", format: "xml", onNewTasks: "allow", onRemovedTasks: "warn"},
		"negative pct":      {baselinePath: "a", currentPath: "b", maxRegressionPct: -1, format: gateFormatHuman, onNewTasks: "allow", onRemovedTasks: "warn"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runGate(&buf, opts)
			var ec *ExitCodeError
			if !errors.As(err, &ec) || ec.Code != GateExitConfigError {
				t.Fatalf("expected config error exit %d, got %v", GateExitConfigError, err)
			}
		})
	}
}

func TestGate_ConfigErrorOnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	bp := filepath.Join(dir, "does-not-exist.json")
	cp := filepath.Join(dir, "also-missing.json")
	var buf bytes.Buffer
	err := runGate(&buf, defaultOpts(bp, cp))
	var ec *ExitCodeError
	if !errors.As(err, &ec) || ec.Code != GateExitConfigError {
		t.Fatalf("expected config error exit %d, got %v", GateExitConfigError, err)
	}
}

func TestGate_FormatGitHubActionsEmitsAnnotations(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.95, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusPassed},
	})
	curr := makeOutcome(0.50, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusFailed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	summaryFile := filepath.Join(dir, "step-summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summaryFile)

	opts := defaultOpts(bp, cp)
	opts.format = gateFormatGitHubActions
	var buf bytes.Buffer
	_ = runGate(&buf, opts) // error is expected; we want the output side-effects

	got := buf.String()
	if !strings.Contains(got, "::error title=Waza regression::") {
		t.Errorf("expected regression annotation, got:\n%s", got)
	}
	if !strings.Contains(got, "::error title=Golden task failed::") {
		t.Errorf("expected golden annotation, got:\n%s", got)
	}

	sum, err := os.ReadFile(summaryFile)
	if err != nil {
		t.Fatalf("step summary file not written: %v", err)
	}
	if !strings.Contains(string(sum), "Waza regression gate") {
		t.Errorf("expected markdown summary in step summary file, got:\n%s", sum)
	}
}

func TestGate_FormatMarkdownAndHuman(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	curr := makeOutcome(0.90, []models.TestOutcome{{TestID: "t1", Status: models.StatusPassed}})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	t.Run("markdown", func(t *testing.T) {
		opts := defaultOpts(bp, cp)
		opts.format = gateFormatMarkdown
		var buf bytes.Buffer
		if err := runGate(&buf, opts); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !strings.Contains(buf.String(), "Waza regression gate") {
			t.Errorf("missing markdown header: %s", buf.String())
		}
	})

	t.Run("human", func(t *testing.T) {
		opts := defaultOpts(bp, cp)
		opts.format = gateFormatHuman
		var buf bytes.Buffer
		if err := runGate(&buf, opts); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !strings.Contains(buf.String(), "REGRESSION GATE") {
			t.Errorf("missing human header: %s", buf.String())
		}
	})
}

func TestGate_ReportFieldsForJSON(t *testing.T) {
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "drop", Status: models.StatusPassed},
	})
	curr := makeOutcome(0.85, []models.TestOutcome{
		{TestID: "t1", Status: models.StatusPassed},
		{TestID: "new", Status: models.StatusPassed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.onNewTasks = gatePolicyWarn
	opts.onRemovedTasks = gatePolicyWarn
	var buf bytes.Buffer
	_ = runGate(&buf, opts)

	var r GateReport
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.NewTasks) != 1 || r.NewTasks[0] != "new" {
		t.Errorf("expected new=[new], got %v", r.NewTasks)
	}
	if len(r.RemovedTasks) != 1 || r.RemovedTasks[0] != "drop" {
		t.Errorf("expected removed=[drop], got %v", r.RemovedTasks)
	}
	if r.PassRateDelta >= 0 {
		t.Errorf("expected negative pass-rate delta, got %v", r.PassRateDelta)
	}
	if r.RegressionPct <= 0 {
		t.Errorf("expected positive regression pct, got %v", r.RegressionPct)
	}
}

func TestGate_FormatGitHubActionsDemotesGoldenWhenPolicyRelaxed(t *testing.T) {
	// With --golden-must-pass=false, a golden task failure is non-blocking,
	// so the GitHub Actions formatter must emit ::warning:: rather than
	// ::error:: for it — otherwise CI would surface a misleading error.
	dir := t.TempDir()
	base := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusPassed},
	})
	curr := makeOutcome(0.90, []models.TestOutcome{
		{TestID: "g1", Golden: true, Status: models.StatusFailed},
	})
	bp := writeOutcomeFile(t, dir, "b.json", base)
	cp := writeOutcomeFile(t, dir, "c.json", curr)

	opts := defaultOpts(bp, cp)
	opts.format = gateFormatGitHubActions
	opts.goldenMustPass = false

	var buf bytes.Buffer
	if err := runGate(&buf, opts); err != nil {
		t.Fatalf("expected pass when golden policy relaxed, got %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "::warning title=Golden task failed (non-blocking)") {
		t.Errorf("expected warning-level golden annotation, got:\n%s", got)
	}
	if strings.Contains(got, "::error title=Golden task failed::") {
		t.Errorf("did not expect error-level golden annotation when policy relaxed, got:\n%s", got)
	}
}

func TestGoldenYAMLFieldRoundtrip(t *testing.T) {
	// Sanity check that the YAML/JSON tags are wired correctly so a TestCase
	// with `golden: true` is detected and a TestOutcome carries `golden: true`.
	tc := models.TestCase{TestID: "t", Golden: true}
	if !tc.Golden || tc.TestID != "t" {
		t.Fatalf("TestCase fields not set: %+v", tc)
	}
	out, err := json.Marshal(models.TestOutcome{TestID: "t", Golden: true, Status: models.StatusPassed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"golden":true`) {
		t.Errorf("expected golden in JSON, got %s", out)
	}
}
