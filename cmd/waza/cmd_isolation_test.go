package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionisolation"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/require"
)

func writeIsolationFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestIsolationPreflightCommandReportsDiscoveryAndRejection(t *testing.T) {
	dir := t.TempDir()
	request := writeIsolationFile(t, dir, "request.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"subprocess":"restricted"}}`)
	host := writeIsolationFile(t, dir, "host.json",
		`{"kind":"waza.execution-isolation-host","schema_version":"1.0","enforced_by":"fixture host claim","dimensions":{"subprocess":{}}}`)

	discover := newRootCommand()
	var discovered bytes.Buffer
	discover.SetOut(&discovered)
	discover.SetArgs([]string{"isolation", "preflight"})
	require.NoError(t, discover.Execute())
	report, err := executionisolation.ParseReport(discovered.Bytes())
	require.NoError(t, err)
	require.Equal(t, executionisolation.Admitted, report.Decision)

	// The host claims a subprocess boundary but the real probe starts a child,
	// so the claim is disproved and the request is rejected.
	output := filepath.Join(dir, "report.json")
	reject := newRootCommand()
	var rejected bytes.Buffer
	reject.SetOut(&rejected)
	reject.SetArgs([]string{"isolation", "preflight", "--request", request, "--host", host, "-o", output})
	err = reject.Execute()
	exitErr, ok := err.(*ExitCodeError)
	require.True(t, ok, "expected exit code error, got %v", err)
	require.Equal(t, ExitTestFailed, exitErr.Code)
	saved, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, rejected.String(), string(saved))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(saved, &doc))
	require.Equal(t, "rejected", doc["decision"])

	invalid := newRootCommand()
	invalid.SetOut(&bytes.Buffer{})
	invalid.SetArgs([]string{"isolation", "preflight", "--request", host})
	err = invalid.Execute()
	exitErr, ok = err.(*ExitCodeError)
	require.True(t, ok)
	require.Equal(t, ExitError, exitErr.Code)
	require.ErrorIs(t, err, executionisolation.ErrInvalid)
}

func TestRunRejectsUnavailableIsolationBeforeExecution(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	dir := t.TempDir()
	request := writeIsolationFile(t, dir, "request.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"network":"restricted"}}`)
	reportPath := filepath.Join(dir, "isolation.json")
	executed := false
	newBenchmarkRunner = func(cfg *config.EvalConfig, engine execution.AgentEngine, opts ...orchestration.RunnerOption) benchmarkRunner {
		executed = true
		return orchestration.NewEvalRunner(cfg, engine, opts...)
	}

	cmd := newRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", createTestSpec(t, "mock"), "--isolation-request", request, "--isolation-report", reportPath, "--output", filepath.Join(dir, "results.json")})
	err := cmd.Execute()
	require.True(t, executionisolation.IsRejected(err), "expected rejection, got %v", err)
	require.False(t, executed, "no benchmark may start when a requested boundary is unavailable")
	_, statErr := os.Stat(filepath.Join(dir, "results.json"))
	require.True(t, os.IsNotExist(statErr))
	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	report, err := executionisolation.ParseReport(data)
	require.NoError(t, err)
	require.Equal(t, executionisolation.Rejected, report.Decision)

	resetRunGlobals()
	cmd = newRootCommand()
	cmd.SetArgs([]string{"run", createTestSpec(t, "mock"), "--isolation-report", reportPath})
	require.ErrorContains(t, cmd.Execute(), "--isolation-report requires")
}

func TestIsolationReportMustNotReplaceRunFiles(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	dir := t.TempDir()
	request := writeIsolationFile(t, dir, "request.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"network":"restricted"}}`)
	spec := createTestSpec(t, "mock")
	before, err := os.ReadFile(spec)
	require.NoError(t, err)

	cmd := newRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", spec, "--isolation-request", request, "--isolation-report", spec})
	err = cmd.Execute()
	require.ErrorContains(t, err, "must not be the same file")
	after, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.Equal(t, before, after)

	resetRunGlobals()
	cmd = newRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"isolation", "preflight", "--request", request, "-o", request})
	require.ErrorContains(t, cmd.Execute(), "must not be the same file")
}

func TestIsolationReportNeverReplacesSelectedEvalsOrOutputs(t *testing.T) {
	t.Cleanup(resetRunGlobals)
	dir := t.TempDir()
	rejectedRequest := writeIsolationFile(t, dir, "rejected.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"network":"restricted"}}`)
	admittedRequest := writeIsolationFile(t, dir, "admitted.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{}}`)
	root := t.TempDir()
	skillDir := filepath.Join(root, "skill")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: skill\ndescription: test\n---\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "tasks", "task.yaml"), []byte("id: task\nname: Task\ninputs:\n  prompt: hi\n"), 0o600))
	evalPath := filepath.Join(skillDir, "eval.yaml")
	evalBody := []byte("name: skill\nskill: skill\nversion: \"1.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n  executor: mock\n  model: test-model\ntasks:\n  - \"tasks/*.yaml\"\n")
	require.NoError(t, os.WriteFile(evalPath, evalBody, 0o600))

	run := func(args ...string) error {
		resetRunGlobals()
		cmd := newRootCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append([]string{"run"}, args...))
		return cmd.Execute()
	}

	for _, request := range []string{rejectedRequest, admittedRequest} {
		err := run("--discover", root, "--isolation-request", request, "--isolation-report", evalPath)
		require.ErrorContains(t, err, "is not a waza.execution-isolation-preflight report")
		after, readErr := os.ReadFile(evalPath)
		require.NoError(t, readErr)
		require.Equal(t, evalBody, after, "a selected eval must never be replaced by the report")
	}

	spec := createTestSpec(t, "mock")
	results := filepath.Join(dir, "results.json")
	for name, tc := range map[string]struct {
		report string
		args   []string
		want   string
	}{
		"junit reporter": {filepath.Join(dir, "junit.xml"), []string{"--reporter", "junit:" + filepath.Join(dir, "junit.xml")}, "must not be the same file"},
		"derived output": {filepath.Join(dir, "results_summary.json"), []string{"--output", results}, "derived from --output"},
		"output dir":     {filepath.Join(dir, "out", "isolation.json"), []string{"--output-dir", filepath.Join(dir, "out")}, "outside --output-dir"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(append([]string{spec, "--isolation-request", admittedRequest, "--isolation-report", tc.report}, tc.args...)...)
			require.ErrorContains(t, err, tc.want)
			_, statErr := os.Stat(tc.report)
			require.True(t, os.IsNotExist(statErr), "the report must not be written when an output would replace it")
		})
	}

	previous := filepath.Join(dir, "previous.json")
	previousBody := []byte(`{"kind":"waza.execution-isolation-preflight","schema_version":"1.0"}`)
	require.NoError(t, os.WriteFile(previous, previousBody, 0o644))
	err := run(spec, "--isolation-request", admittedRequest, "--isolation-report", previous, "--otel-exporter", "file", "--otel-file", previous)
	require.ErrorContains(t, err, "must not be the same file")
	after, err := os.ReadFile(previous)
	require.NoError(t, err)
	require.Equal(t, previousBody, after, "the telemetry exporter must not truncate the previous report")

	compatReport := filepath.Join(dir, "compat.json")
	err = run(spec, "--runner-compat", "--isolation-request", admittedRequest, "--isolation-report", compatReport)
	require.ErrorContains(t, err, "runner compatibility cannot preserve")
	_, statErr := os.Stat(compatReport)
	require.True(t, os.IsNotExist(statErr), "runner compatibility must not publish an isolation report")

	report := filepath.Join(dir, "published.json")
	require.NoError(t, os.WriteFile(report, []byte("replaced"), 0o644))
	publishedIsolationReport = &struct {
		path string
		data []byte
	}{report, []byte("original")}
	require.ErrorContains(t, verifyPublishedIsolationReport(), "was replaced or removed during the run")
}

type stubIsolationProvider struct{ dimension executionisolation.Dimension }

func (stubIsolationProvider) Name() string { return "stub-sandbox" }

func (p stubIsolationProvider) Discover(context.Context) executionisolation.Discovery {
	return executionisolation.Discovery{Status: executionisolation.ProviderAvailable, EnforcedBy: "stub", Claims: []executionisolation.Claim{{
		Dimension: p.dimension,
		Probe: func(context.Context) executionisolation.ProbeResult {
			return executionisolation.ProbeResult{Outcome: executionisolation.Enforced, Detail: "stub enforcement"}
		},
	}}}
}

func TestCheckSpecIsolationRejectsMCPServerEnv(t *testing.T) {
	t.Cleanup(func() { admittedIsolation = nil })
	spec := &models.EvalSpec{}
	spec.Config.ServerConfigs = map[string]any{
		"tools":  map[string]any{"command": "srv", "env": map[string]any{"TOKEN": "x"}},
		"cased":  map[string]any{"command": "srv", "Env": map[string]any{"API_KEY": "x"}},
		"plain":  map[string]any{"command": "srv"},
		"remote": map[string]any{"type": "http", "url": "https://example.test/mcp", "env": map[string]any{"IGNORED": "x"}},
	}
	admittedIsolation = nil
	require.NoError(t, checkSpecIsolation(spec))

	admittedIsolation = &executionisolation.Report{Dimensions: []executionisolation.Capability{
		{Dimension: executionisolation.Network, Effective: executionisolation.EffectiveRestricted},
	}}
	require.NoError(t, checkSpecIsolation(spec), "env injection only matters when environment is restricted")

	admittedIsolation.Dimensions = append(admittedIsolation.Dimensions, executionisolation.Capability{
		Dimension: executionisolation.Environment, Effective: executionisolation.EffectiveRestricted,
	})
	err := checkSpecIsolation(spec)
	require.ErrorContains(t, err, "mcp_servers cased, tools set environment variables")
	exitErr, ok := err.(*ExitCodeError)
	require.True(t, ok)
	require.Equal(t, ExitError, exitErr.Code)
}

func TestRunChecksEverySpecIsolationBeforeExecution(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	isolationProviders = func(*executionisolation.HostDeclaration, string) []executionisolation.Provider {
		return []executionisolation.Provider{stubIsolationProvider{executionisolation.Environment}}
	}
	dir := t.TempDir()
	request := writeIsolationFile(t, dir, "request.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"environment":"restricted"}}`)
	root := t.TempDir()
	writeSkill := func(name, extraConfig string) {
		skillDir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "tasks"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: test\n---\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "tasks", "task.yaml"), []byte("id: task\nname: Task\ninputs:\n  prompt: hi\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "eval.yaml"), []byte("name: "+name+"\nskill: "+name+"\nversion: \"1.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n  executor: mock\n  model: test-model\n"+extraConfig+"tasks:\n  - \"tasks/*.yaml\"\n"), 0o600))
	}
	writeSkill("a-clean", "")
	writeSkill("b-tools", "  mcp_servers:\n    tools:\n      command: srv\n      Env:\n        API_KEY: x\n")
	spec, err := models.LoadEvalSpec(filepath.Join(root, "b-tools", "eval.yaml"))
	require.NoError(t, err)
	require.Contains(t, spec.Config.ServerConfigs, "tools", "fixture must place mcp_servers under config")

	executed := false
	newBenchmarkRunner = func(cfg *config.EvalConfig, engine execution.AgentEngine, opts ...orchestration.RunnerOption) benchmarkRunner {
		executed = true
		return orchestration.NewEvalRunner(cfg, engine, opts...)
	}
	reportPath := filepath.Join(dir, "isolation.json")
	cmd := newRootCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", "--discover", root, "--isolation-request", request, "--isolation-report", reportPath})
	err = cmd.Execute()
	require.ErrorContains(t, err, "mcp_servers tools set environment variables")
	require.False(t, executed, "an earlier spec must not run when a later spec breaks the admitted isolation")
	_, statErr := os.Stat(reportPath)
	require.True(t, os.IsNotExist(statErr), "an admission withdrawn by a spec check must not be published")
}

func TestWriteIsolationReportNeverWritesThroughForeignFiles(t *testing.T) {
	dir := t.TempDir()
	report := []byte(`{"kind":"waza.execution-isolation-preflight","schema_version":"1.0","decision":"admitted"}`)

	fresh := filepath.Join(dir, "fresh.json")
	require.NoError(t, writeIsolationReport(fresh, report))
	data, err := os.ReadFile(fresh)
	require.NoError(t, err)
	require.Equal(t, report, data)

	updated := []byte(`{"kind":"waza.execution-isolation-preflight","schema_version":"1.0","decision":"rejected"}`)
	require.NoError(t, writeIsolationReport(fresh, updated), "an existing report is replaced")
	data, err = os.ReadFile(fresh)
	require.NoError(t, err)
	require.Equal(t, updated, data)

	foreign := filepath.Join(dir, "eval.yaml")
	foreignBody := []byte("name: keep\n")
	require.NoError(t, os.WriteFile(foreign, foreignBody, 0o644))
	require.ErrorContains(t, writeIsolationReport(foreign, report), "not a waza.execution-isolation-preflight report")

	link := filepath.Join(dir, "link.json")
	target := filepath.Join(dir, "target.json")
	require.NoError(t, os.WriteFile(target, updated, 0o644))
	if err := os.Symlink(target, link); err == nil {
		require.ErrorContains(t, writeIsolationReport(link, report), "not a regular file")
		data, err = os.ReadFile(target)
		require.NoError(t, err)
		require.Equal(t, updated, data, "a symlinked destination must not be written through")
	}

	data, err = os.ReadFile(foreign)
	require.NoError(t, err)
	require.Equal(t, foreignBody, data)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".waza-isolation-report-", "temporary files are removed")
	}
}
