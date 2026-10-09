package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/stretchr/testify/require"
)

type preflightEngineSpy struct {
	initialize, execute, shutdown, usage atomic.Int64
}

func (s *preflightEngineSpy) Initialize(context.Context) error {
	s.initialize.Add(1)
	return nil
}

func (s *preflightEngineSpy) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	s.execute.Add(1)
	return &execution.ExecutionResponse{Success: true}, nil
}

func (s *preflightEngineSpy) Shutdown(context.Context) error {
	s.shutdown.Add(1)
	return nil
}

func (s *preflightEngineSpy) SessionUsage(string) *models.UsageStats {
	s.usage.Add(1)
	return nil
}

type preflightTransportSpy struct{ calls atomic.Int64 }

func (s *preflightTransportSpy) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls.Add(1)
	return nil, errors.New("network is forbidden in preflight")
}

func TestPreflightExactlyZeroEngineAndLiveCalls(t *testing.T) {
	// This replaces the production run/spec engine factories, not a callback
	// invented for preflight. The full root command is exercised below.
	engine := &preflightEngineSpy{}
	var constructions atomic.Int64
	oldRun, oldSpec := newRunEngine, newSpecVerifyEngine
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		constructions.Add(1)
		return engine, nil
	}
	newSpecVerifyEngine = func(string) execution.AgentEngine {
		constructions.Add(1)
		return engine
	}
	t.Cleanup(func() { newRunEngine, newSpecVerifyEngine = oldRun, oldSpec })
	transport := &preflightTransportSpy{}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	oldVersion := version
	version = "0.1.0"
	t.Cleanup(func() { version = oldVersion })

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	log := filepath.Join(dir, "subprocess-calls")
	if runtime.GOOS != "windows" {
		for _, name := range []string{"python3", "python", "node", "git", "copilot", "offline-service"} {
			require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf 'call\\n' >> '"+log+"'\nexit 0\n"), 0700))
		}
	}
	t.Setenv("PATH", bin)
	tmp := filepath.Join(dir, "tmp")
	require.NoError(t, os.Mkdir(tmp, 0700))
	t.Setenv("TMPDIR", tmp)
	t.Setenv("WAZA_MODULE_CACHE", filepath.Join(dir, "cache"))
	eval := `schemaVersion: "1.4"
name: offline
skill: example
config:
  executor: copilot-sdk
  model: never-call
  trials_per_task: 1
  timeout_seconds: 10
  mcp_servers:
    live: {type: http, url: "https://never-contact.invalid", headers: {Authorization: private-token}}
    service: {command: offline-service}
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
hooks:
  before_run: [{command: offline-service}]
mcp_mocks:
  - name: fixture
    tools:
      read:
        responses: [{return: {value: ready}}]
command_mocks:
  - name: example-cli
    responses: [{args: [], stdout: ready}]
`
	task := `id: offline
name: Offline
inputs: {prompt: inspect the fixture}
graders:
  - name: script
    type: code
    config: {assertions: ["true"], language: javascript}
  - name: program
    type: program
    config: {command: offline-service}
  - name: judge
    type: prompt
    config: {prompt: "never judge"}
  - name: external-schema
    type: json_schema
    config: {schema: {$ref: 'https://never-contact.invalid/schema'}}
`
	path := filepath.Join(dir, "eval.yaml")
	taskPath := filepath.Join(dir, "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
	require.NoError(t, os.WriteFile(taskPath, []byte(task), 0600))
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	scenario := strings.ReplaceAll(strings.ReplaceAll(eval, `schemaVersion: "1.4"`, `schemaVersion: "2.0"`), "skill: example", "scenario: local-workflow")
	require.NoError(t, os.WriteFile(scenarioPath, []byte(scenario), 0600))
	for _, tc := range []struct {
		name, input string
		strict      bool
		wantFailure bool
	}{
		{"configured live mocks hooks scripts and judges", path, false, false},
		{"scenario no ambient discovery", scenarioPath, false, false},
		{"strict unresolved", path, true, true},
		{"missing eval", filepath.Join(dir, "absent.yaml"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCommand()
			cmd, _, err := root.Find([]string{"preflight"})
			require.NoError(t, err)
			require.False(t, shouldRunUpdateCheck(cmd, false))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			args := []string{"preflight", tc.input, "--format", "json"}
			if tc.strict {
				args = append(args, "--strict")
			}
			root.SetArgs(args)
			err = root.Execute()
			if tc.wantFailure {
				var exit *ExitCodeError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 1, exit.Code)
			} else {
				require.NoError(t, err)
			}
			var report preflight.Report
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			require.Equal(t, preflight.ReportKind, report.Kind)
			require.NotContains(t, out.String(), "private-token")
			require.NotContains(t, out.String(), "never-contact.invalid")
		})
	}
	require.Zero(t, constructions.Load(), "engine construction")
	require.Zero(t, engine.initialize.Load(), "engine initialization")
	require.Zero(t, engine.execute.Load(), "model execution")
	require.Zero(t, engine.shutdown.Load(), "engine shutdown")
	require.Zero(t, engine.usage.Load(), "engine usage/live RPC")
	require.Zero(t, transport.calls.Load(), "network/update/live-service calls")
	if runtime.GOOS != "windows" {
		_, err := os.Stat(log)
		require.True(t, os.IsNotExist(err), "subprocess/service calls must be exactly zero")
	}
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries, "no service configs, shims, workspaces, or temp fixture writes")
	evalAfter, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, eval, string(evalAfter))
	taskAfter, err := os.ReadFile(taskPath)
	require.NoError(t, err)
	require.Equal(t, task, string(taskAfter))
}

func TestPreflightHumanAndErrors(t *testing.T) {
	cmd := newPreflightCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{filepath.Join(t.TempDir(), "absent.yaml")})
	var exit *ExitCodeError
	require.ErrorAs(t, cmd.Execute(), &exit)
	require.Contains(t, out.String(), "Offline preflight")
	require.Contains(t, out.String(), "invalid eval.read")
	cmd = newPreflightCommand()
	cmd.SetArgs([]string{"absent.yaml", "--format", "unknown"})
	require.ErrorContains(t, cmd.Execute(), "invalid --format")
	cmd = newPreflightCommand()
	cmd.SetArgs(nil)
	require.Error(t, cmd.Execute())
}

type preflightFailWriter struct {
	failAt, calls int
}

func (w *preflightFailWriter) Write(data []byte) (int, error) {
	w.calls++
	if w.failAt > 0 && w.calls != w.failAt {
		return len(data), nil
	}
	return 0, errors.New("write failed")
}

func TestPreflightOutputErrors(t *testing.T) {
	report := &preflight.Report{
		Tasks:        []preflight.TaskPlan{{ID: "task", Requirements: []preflight.RequirementPlan{{}}}},
		Capabilities: []preflight.Capability{{Name: "runtime"}},
		Dependencies: []preflight.Dependency{{Name: "dependency"}},
		Diagnostics:  []preflight.Diagnostic{{Code: "diagnostic"}},
	}
	for i, section := range []string{"summary", "task", "requirement", "capability", "dependency", "diagnostic"} {
		w := &preflightFailWriter{failAt: i + 1}
		require.ErrorContains(t, renderPreflightHuman(w, report), "writing preflight "+section)
		require.Equal(t, i+1, w.calls)
	}
	cmd := newPreflightCommand()
	cmd.SetOut(&preflightFailWriter{})
	cmd.SetArgs([]string{"absent.yaml", "--format", "json"})
	require.ErrorContains(t, cmd.Execute(), "writing preflight JSON")
}

func TestPreflightDoesNotChangeOtherUpdateChecks(t *testing.T) {
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	root := newRootCommand()
	for _, name := range []string{"run", "check", "grade"} {
		cmd, _, err := root.Find([]string{name})
		require.NoError(t, err)
		require.True(t, shouldRunUpdateCheck(cmd, false), strings.Join([]string{name, "retains prior behavior"}, " "))
	}
}
