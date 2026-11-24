package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/internal/workspace"
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

func TestPreflightTaskEnvironmentWarnOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`schemaVersion: "1.4"
name: environment
skill: example
config: {executor: mock, model: harness, trials_per_task: 1, timeout_seconds: 10}
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`id: env-task
name: Environment
inputs:
  prompt: Say ready.
  environment: {PRIVATE_SETTING: sensitive-value}
`), 0600))
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint("strict=", strict), func(t *testing.T) {
			root := newRootCommand()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			args := []string{"preflight", path, "--format", "json"}
			if strict {
				args = append(args, "--strict")
			}
			root.SetArgs(args)
			err := root.Execute()
			if strict {
				require.Error(t, err, "existing --strict policy remains in effect")
			} else {
				require.NoError(t, err)
			}
			var report preflight.Report
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			require.Len(t, report.Tasks, 1)
			var found bool
			for _, diagnostic := range report.Diagnostics {
				if diagnostic.Code == "task.environment" {
					found = true
					require.Equal(t, preflight.Unsupported, diagnostic.State)
					require.Equal(t, "warning", diagnostic.Severity)
				}
			}
			require.True(t, found)
			require.NotContains(t, out.String(), "PRIVATE_SETTING")
			require.NotContains(t, out.String(), "sensitive-value")
		})
	}
}

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
    config:
      prompt: "never judge"
      evidence_context:
        non_secret_literals: ["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fixture@example.invalid, PROJ-1]
  - name: external-schema
    type: json_schema
    config: {schema: {$ref: 'https://never-contact.invalid/schema'}}
`
	path := filepath.Join(dir, "eval.yaml")
	taskPath := filepath.Join(dir, "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
	triggerPath := filepath.Join(dir, "trigger_tests.yaml")
	require.NoError(t, os.WriteFile(triggerPath, []byte(admissionTrigger), 0600))
	malformedMCPPath := filepath.Join(dir, "malformed-mcp.yaml")
	require.NoError(t, os.WriteFile(malformedMCPPath, []byte(strings.ReplaceAll(eval,
		"headers: {Authorization: private-token}", "headers: [private-token]")), 0600))
	nullScenarioPath := filepath.Join(dir, "null-scenario.yaml")
	require.NoError(t, os.WriteFile(nullScenarioPath, []byte("scenario: null\n"+eval), 0600))
	require.NoError(t, os.WriteFile(taskPath, []byte(task), 0600))
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	scenario := strings.ReplaceAll(strings.ReplaceAll(eval, `schemaVersion: "1.4"`, `schemaVersion: "2.0"`), "skill: example", "scenario: local-workflow")
	require.NoError(t, os.WriteFile(scenarioPath, []byte(scenario), 0600))
	externalPath := filepath.Join(dir, "readable-schema.json")
	require.NoError(t, os.WriteFile(externalPath, []byte(`{"type":"object"}`), 0600))
	fileRef := testutil.FileURL(externalPath)
	argumentGrader := "  - name: argument\n    type: tool_constraint\n    config:\n      allow_only:\n        - tool: read\n          args:\n            value:\n              json_schema:\n                $ref: '" + fileRef + "'\n"
	eagerEvalPath := filepath.Join(dir, "eager-eval.yaml")
	require.NoError(t, os.WriteFile(eagerEvalPath, []byte(eval+"\ngraders:\n"+argumentGrader), 0600))
	eagerTaskPath := filepath.Join(dir, "eager-task.yaml")
	require.NoError(t, os.WriteFile(eagerTaskPath, []byte(strings.Split(task, "graders:")[0]+"graders:\n"+argumentGrader), 0600))
	eagerTaskEvalPath := filepath.Join(dir, "eager-task-eval.yaml")
	require.NoError(t, os.WriteFile(eagerTaskEvalPath, []byte(strings.ReplaceAll(eval, "task.yaml", "eager-task.yaml")), 0600))
	unsupportedPath := filepath.Join(dir, "unsupported.yaml")
	unsupportedEval := `schemaVersion: "2.0"
name: offline-unsupported
scenario: mock-mcp
config:
  executor: mock
  model: harness
  trials_per_task: 1
  timeout_seconds: 10
  mcp_servers:
    live: {command: offline-service}
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [unsupported-task.yaml]
`
	require.NoError(t, os.WriteFile(unsupportedPath, []byte(unsupportedEval), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unsupported-task.yaml"),
		[]byte("id: unsupported\nname: Unsupported\ninputs: {prompt: inspect}\ngraders: [{name: answer, type: text, config: {contains: [ready]}}]\n"), 0600))
	for _, tc := range []struct {
		name, input string
		strict      bool
		wantFailure bool
		canceled    bool
	}{
		{"configured live mocks hooks scripts and judges", path, false, false, false},
		{"scenario no ambient discovery", scenarioPath, false, false, false},
		{"strict unresolved", path, true, true, false},
		{"malformed live MCP configuration", malformedMCPPath, false, true, false},
		{"declared null scenario fails without calls", nullScenarioPath, false, true, false},
		{"unsupported capability warns", unsupportedPath, false, false, false},
		{"unsupported capability strict", unsupportedPath, true, true, false},
		{"canceled command context retains offline inspection", path, false, false, true},
		{"canceled strict context retains failure report", path, true, true, true},
		{"eager eval argument schema", eagerEvalPath, false, false, false},
		{"eager task argument schema", eagerTaskEvalPath, false, false, false},
		{"eager schema strict", eagerTaskEvalPath, true, true, false},
		{"missing eval", filepath.Join(dir, "absent.yaml"), false, true, false},
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
			ctx := context.Background()
			if tc.canceled {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			err = root.ExecuteContext(ctx)
			if tc.wantFailure {
				var exit *ExitCodeError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 1, exit.Code)
			} else {
				require.NoError(t, err, out.String())
			}
			var report preflight.Report
			require.NoError(t, json.Unmarshal(out.Bytes(), &report))
			require.Equal(t, preflight.ReportKind, report.Kind)
			require.NotContains(t, out.String(), "private-token")
			require.NotContains(t, out.String(), "never-contact.invalid")
			require.Equal(t, tc.wantFailure, report.Failed(tc.strict))
			if tc.input == unsupportedPath {
				require.Contains(t, report.Capabilities, preflight.Capability{
					Name: "mcp_servers", State: preflight.Unsupported,
					Meaning: "Static capability only; not proof of configuration enforcement or task success.",
				})
				var found bool
				for _, diagnostic := range report.Diagnostics {
					require.NotEqual(t, preflight.Invalid, diagnostic.State, "default warning must not conceal invalid configuration")
					if diagnostic.Code == "runtime.mcp" {
						found = true
						require.Equal(t, preflight.Unsupported, diagnostic.State)
						require.Equal(t, "warning", diagnostic.Severity)
					}
				}
				require.True(t, found, "requested unsupported capability must have a diagnostic")
			}
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
	triggerAfter, err := os.ReadFile(triggerPath)
	require.NoError(t, err)
	require.Equal(t, admissionTrigger, string(triggerAfter), "trigger discovery must remain read-only")
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

func TestPreflightWorkspaceJSONUsesNativeDiscovery(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "reviewed")
	evalDir := filepath.Join(dir, "evals", "reviewed")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.MkdirAll(evalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: reviewed-task\nname: Reviewed\ninputs: {prompt: ready}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte(`schemaVersion: "1.4"
name: reviewed
skill: reviewed
config: {executor: mock, model: harness, trials_per_task: 1, timeout_seconds: 10}
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: skills\n  evals: evals\n"), 0o600))
	t.Chdir(dir)

	cmd := newPreflightCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--workspace", "--format", "json"})
	require.NoError(t, cmd.Execute())
	var reports []*preflight.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &reports))
	require.Len(t, reports, 1)
	require.Equal(t, filepath.Join(evalDir, "eval.yaml"), reports[0].Source)
	require.Len(t, reports[0].Tasks, 1)
}

func TestPreflightWorkspaceResolvesConfiguredPathsFromConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "custom-skills", "reviewed")
	evalDir := filepath.Join(dir, "custom-evals", "reviewed")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.MkdirAll(evalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: reviewed-task\nname: Reviewed\ninputs: {prompt: ready}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "suite.yml"), []byte(`schemaVersion: "1.4"
name: reviewed
skill: reviewed
config: {executor: mock, model: harness, trials_per_task: 1, timeout_seconds: 10}
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(`paths:
  skills: custom-skills
  evals: custom-evals
files:
  evalFile: suite.yml
`), 0o600))
	nested := filepath.Join(dir, "nested", "workspace")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	t.Chdir(nested)

	cmd := newPreflightCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--workspace", "--format", "json"})
	require.NoError(t, cmd.Execute())
	var reports []*preflight.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &reports))
	require.Len(t, reports, 1)
	require.Equal(t, filepath.Join(evalDir, "suite.yml"), reports[0].Source)
	require.Len(t, reports[0].Tasks, 1)
}

func TestPreflightWorkspaceInspectsTargetFreeAndChildEvals(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	t.Chdir(dir)
	scaffold := newRootCommand()
	scaffold.SetOut(&bytes.Buffer{})
	scaffold.SetArgs([]string{"new", "eval", "inventory", "--scenario"})
	require.NoError(t, scaffold.Execute())
	scenario := filepath.Join(dir, "evals", "inventory", "eval.yaml")
	require.FileExists(t, scenario)

	// A root SKILL.md makes detection single-skill, hiding the child skill.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: parent\n---\n# parent\n"), 0o600))
	childSkill := filepath.Join(dir, "skills", "child")
	require.NoError(t, os.MkdirAll(childSkill, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(childSkill, "SKILL.md"), []byte("---\nname: child\n---\n# child\n"), 0o600))
	childEval := filepath.Join(dir, "evals", "child", "eval.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(childEval), 0o700))
	require.NoError(t, os.WriteFile(childEval, []byte("name: child\nconfig: [invalid]\n"), 0o600))
	ws, err := workspace.DetectContext(dir)
	require.NoError(t, err)
	require.Equal(t, workspace.ContextSingleSkill, ws.Type, "fixture must hide the child skill from discovery")

	cmd := newPreflightCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--workspace", "--format", "json"})
	var exit *ExitCodeError
	require.ErrorAs(t, cmd.Execute(), &exit, "invalid child evaluation must fail workspace preflight")
	var reports []*preflight.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &reports), out.String())
	sources := map[string]*preflight.Report{}
	for _, report := range reports {
		sources[report.Source] = report
	}
	require.Contains(t, sources, scenario)
	require.Contains(t, sources, childEval)
	require.Len(t, reports, 2)
	require.True(t, sources[childEval].Failed(false))
}

func TestPreflightWorkspaceRequiredSkillsMatchExplicitRun(t *testing.T) {
	const skill = "---\nname: parent\ndescription: Parent skill\n---\n# parent\n"
	const eval = `schemaVersion: "1.4"
name: inventory
skill: parent
version: "1.0"
config:
  executor: mock
  model: harness
  trials_per_task: 1
  timeout_seconds: 10
  required_skills: [parent]
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
`
	const task = "id: task\nname: Task\ninputs: {prompt: ready}\n"

	for _, layout := range []string{"standalone extra eval", "github skill outside configured root"} {
		t.Run(layout, func(t *testing.T) {
			dir, workflow := initCIFixture(t)
			t.Chdir(dir)
			evalDir := filepath.Join(dir, "evals", "inventory")
			if layout == "standalone extra eval" {
				require.NoError(t, os.Remove(filepath.Join(dir, ".waza.yaml")))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o600))
			} else {
				skillDir := filepath.Join(dir, ".github", "skills", "parent")
				require.NoError(t, os.MkdirAll(skillDir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600))
				evalDir = filepath.Join(dir, "evals", "parent")
			}
			require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o700))
			evalPath := filepath.Join(evalDir, "eval.yaml")
			require.NoError(t, os.WriteFile(evalPath, []byte(eval), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte(task), 0o600))

			check := func(t *testing.T, wantValid bool, wantSkill string) {
				t.Helper()
				single := newPreflightCommand()
				var output bytes.Buffer
				single.SetOut(&output)
				single.SilenceUsage = true
				single.SetArgs([]string{evalPath, "--format", "json"})
				singleErr := single.Execute()
				require.Equal(t, wantValid, singleErr == nil, output.String())
				var report preflight.Report
				require.NoError(t, json.Unmarshal(output.Bytes(), &report))
				require.Equal(t, evalPath, report.Source)
				require.Len(t, report.Tasks, 1)
				var requiredInvalid bool
				for _, d := range report.Diagnostics {
					if d.Code == "skill.required" && d.State == preflight.Invalid {
						requiredInvalid = true
					}
				}
				require.Equal(t, !wantValid, requiredInvalid, "explicit preflight must check the required skill")

				step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
				step.Env["WAZA_INIT_CI_MODE"] = "reject"
				workspaceOut, workspaceErr := initCIShell(t, dir, step.Run, step.Env)
				require.Equal(t, wantValid, workspaceErr == nil, workspaceOut)
				require.Contains(t, workspaceOut, "Offline preflight: "+evalPath)
				require.Equal(t, !wantValid, strings.Contains(workspaceOut, "invalid skill.required"), workspaceOut)
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"), "offline inventory must not construct an engine")

				runOut, runErr := initCIShell(t, dir, `waza run "$EVAL_PATH" --output results.json`, map[string]string{
					"EVAL_PATH": filepath.ToSlash(evalPath), "WAZA_INIT_CI_MODE": "record-skills",
				})
				require.Equal(t, wantValid, runErr == nil, runOut)
				if wantValid {
					require.FileExists(t, filepath.Join(dir, "results.json"))
					paths, err := os.ReadFile(filepath.Join(dir, "session-skill-paths"))
					require.NoError(t, err)
					require.Contains(t, string(paths), wantSkill, "mock execution must receive the required skill path")
					require.NoError(t, os.Remove(filepath.Join(dir, "results.json")))
					require.NoError(t, os.Remove(filepath.Join(dir, "session-skill-paths")))
				} else {
					require.Contains(t, runOut, "required_skills")
					require.NoFileExists(t, filepath.Join(dir, "results.json"))
				}
				if _, err := os.Stat(filepath.Join(dir, "runtime-invoked")); err == nil {
					require.NoError(t, os.Remove(filepath.Join(dir, "runtime-invoked")))
				} else {
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}

			if layout == "standalone extra eval" {
				t.Run("owner exists", func(t *testing.T) { check(t, true, dir) })
				require.NoError(t, os.Remove(filepath.Join(dir, "SKILL.md")))
				t.Run("owner absent", func(t *testing.T) { check(t, false, "") })
			} else {
				t.Run("unconfigured owner", func(t *testing.T) { check(t, false, "") })
				skillDir := filepath.Join(dir, "skills", "parent")
				require.NoError(t, os.MkdirAll(skillDir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600))
				t.Run("configured owner", func(t *testing.T) { check(t, true, skillDir) })
			}
		})
	}
}

func TestPreflightWorkspaceSkillsDirOverridesRequiredSkills(t *testing.T) {
	const skill = "---\nname: %s\ndescription: Local skill\n---\n# %s\n"
	const eval = `schemaVersion: "1.4"
name: inventory
skill: owner
version: "1.0"
config:
  executor: mock
  model: harness
  trials_per_task: 1
  timeout_seconds: 10
  required_skills: [dependency]
%s
metrics: [{name: accuracy, weight: 1, threshold: 0.8}]
tasks: [task.yaml]
`
	for _, tc := range []struct {
		name                       string
		configured, selected       bool
		missingRoot, absolute      bool
		explicit, badExplicit      bool
		disabled                   bool
		noConfig, extraEval        bool
		wantConfigured, wantChosen bool
	}{
		{name: "selected root only", selected: true, wantChosen: true},
		{name: "configured root only", configured: true, wantConfigured: true},
		{name: "both roots and absolute override", configured: true, selected: true, absolute: true, wantConfigured: true, wantChosen: true},
		{name: "neither root"},
		{name: "missing selected root", configured: true, missingRoot: true, wantConfigured: true},
		{name: "no project config", selected: true, noConfig: true, wantChosen: true},
		{name: "extra eval", selected: true, extraEval: true, wantChosen: true},
		{name: "explicit eval directories", explicit: true, wantConfigured: true, wantChosen: true},
		{name: "explicit directories do not fall back", selected: true, explicit: true, badExplicit: true},
		{name: "disabled discovery", disabled: true, missingRoot: true, wantConfigured: true, wantChosen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			writeSkill := func(root, name string) {
				t.Helper()
				skillDir := filepath.Join(dir, root, name)
				require.NoError(t, os.MkdirAll(skillDir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(fmt.Sprintf(skill, name, name)), 0o600))
			}
			writeSkill("skills", "owner")
			if !tc.extraEval {
				writeSkill("custom-skills", "owner")
			}
			if tc.configured {
				writeSkill("skills", "dependency")
			}
			if tc.selected {
				writeSkill("custom-skills", "dependency")
			}
			evalDir := filepath.Join(dir, "evals", "owner")
			require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o700))
			configExtra := ""
			if tc.explicit {
				if tc.badExplicit {
					writeSkill(filepath.Join("evals", "owner", "explicit"), "other")
					configExtra = "  skill_directories: [explicit/other]"
				} else {
					writeSkill(filepath.Join("evals", "owner", "explicit"), "dependency")
					configExtra = "  skill_directories: [explicit/dependency]"
				}
			} else if tc.disabled {
				configExtra = "  disabled_skills: ['*']"
			}
			evalPath := filepath.Join(evalDir, "eval.yaml")
			require.NoError(t, os.WriteFile(evalPath, []byte(fmt.Sprintf(eval, configExtra)), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: task\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
			configPath := filepath.Join(dir, ".waza.yaml")
			writeConfig := func(root string) {
				t.Helper()
				data := []byte(fmt.Sprintf("paths:\n  skills: %s\n  evals: evals\n", root))
				require.NoError(t, os.WriteFile(configPath, data, 0o600))
			}
			selectedRoot := "custom-skills"
			if tc.missingRoot {
				selectedRoot = "missing-skills"
			}
			env := map[string]string{
				"WAZA_INIT_CI_MODE": "reject",
				"REPORT_PATH":       filepath.ToSlash(filepath.Join(dir, "preflight.json")),
				"SKILLS_OVERRIDE":   selectedRoot,
			}
			if tc.absolute {
				env["SKILLS_OVERRIDE"] = filepath.ToSlash(filepath.Join(dir, selectedRoot))
			}
			checkPreflight := func(t *testing.T, override, wantValid bool) {
				t.Helper()
				command := `waza preflight --workspace --format json > "$REPORT_PATH"`
				if override {
					command = `waza preflight --workspace --skills-dir "$SKILLS_OVERRIDE" --format json > "$REPORT_PATH"`
				}
				out, runErr := initCIShell(t, dir, command, env)
				data, err := os.ReadFile(filepath.Join(dir, "preflight.json"))
				require.NoError(t, err, out)
				var reports []preflight.Report
				require.NoError(t, json.Unmarshal(data, &reports), string(data))
				require.Len(t, reports, 1)
				require.Equal(t, evalPath, reports[0].Source)
				require.Len(t, reports[0].Tasks, 1)
				require.Equal(t, "task", reports[0].Tasks[0].ID)
				var invalidRequired, unresolvedRoot bool
				for _, d := range reports[0].Diagnostics {
					invalidRequired = invalidRequired || d.Code == "skill.required" && d.State == preflight.Invalid
					if d.Code == "workspace.skills" && d.State == preflight.Unresolved {
						unresolvedRoot = true
						require.Contains(t, d.Message, "Selected workspace skills folder is unavailable")
					}
				}
				require.Equal(t, !wantValid, invalidRequired, "%s\n%s", out, data)
				require.Equal(t, wantValid, runErr == nil, "%s\n%s", out, data)
				require.Equal(t, override && tc.missingRoot && !tc.explicit && !tc.disabled, unresolvedRoot, string(data))
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
				require.NoError(t, os.Remove(filepath.Join(dir, "preflight.json")))
			}
			checkRun := func(t *testing.T, root string, wantValid bool) {
				t.Helper()
				out, runErr := initCIShell(t, dir, `waza run "$EVAL_PATH" --output results.json`, map[string]string{
					"EVAL_PATH": filepath.ToSlash(evalPath), "WAZA_INIT_CI_MODE": "record-skills",
				})
				require.Equal(t, wantValid, runErr == nil, out)
				if wantValid {
					require.FileExists(t, filepath.Join(dir, "results.json"))
					paths, err := os.ReadFile(filepath.Join(dir, "session-skill-paths"))
					require.NoError(t, err)
					lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(paths), "\r\n", "\n"), "\n"), "\n")
					require.Len(t, lines, 1, "the only task must have one recorded mock session")
					var sessionPaths []string
					if lines[0] != "" {
						sessionPaths = strings.Split(lines[0], "\x1f")
					}
					if tc.disabled {
						require.Empty(t, sessionPaths)
					} else {
						skillPath := filepath.Join(dir, root, "dependency")
						if tc.explicit {
							skillPath = filepath.Join(evalDir, "explicit", "dependency")
						}
						require.Contains(t, sessionPaths, skillPath)
						if tc.explicit {
							require.Equal(t, []string{skillPath}, sessionPaths)
						} else {
							otherRoot := "skills"
							if root == "skills" {
								otherRoot = "custom-skills"
							}
							require.NotContains(t, sessionPaths, filepath.Join(dir, otherRoot, "dependency"))
						}
					}
					require.NoError(t, os.Remove(filepath.Join(dir, "results.json")))
					require.NoError(t, os.Remove(filepath.Join(dir, "session-skill-paths")))
				} else {
					if tc.noConfig && root == "skills" || tc.missingRoot && root == selectedRoot {
						require.Contains(t, out, "required_skills specified but no skill_directories configured")
					} else {
						require.Contains(t, out, "required skills not found")
						require.Contains(t, out, "dependency")
					}
					require.NoFileExists(t, filepath.Join(dir, "results.json"))
				}
				if _, err := os.Stat(filepath.Join(dir, "runtime-invoked")); err == nil {
					require.NoError(t, os.Remove(filepath.Join(dir, "runtime-invoked")))
				} else {
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}

			if !tc.noConfig {
				writeConfig("skills")
			}
			t.Run("configured preflight", func(t *testing.T) { checkPreflight(t, false, tc.wantConfigured) })
			t.Run("configured run", func(t *testing.T) { checkRun(t, "skills", tc.wantConfigured) })
			writeConfig(selectedRoot)
			t.Run("selected-root run", func(t *testing.T) { checkRun(t, selectedRoot, tc.wantChosen) })
			if tc.noConfig {
				require.NoError(t, os.Remove(configPath))
			} else {
				writeConfig("skills")
			}
			t.Run("overridden preflight", func(t *testing.T) { checkPreflight(t, true, tc.wantChosen) })
		})
	}
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
