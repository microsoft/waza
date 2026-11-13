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
