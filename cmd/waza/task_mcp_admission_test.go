package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type taskAdmissionStartupEngine struct {
	execution.AgentEngine
	starts int
}

func (e *taskAdmissionStartupEngine) Initialize(context.Context) error {
	e.starts++
	return errors.New("task admission startup sentinel")
}

func TestRunCommandTaskMCPAdmissionRejectsBeforeStartup(t *testing.T) {
	const task = `id: first
name: first
inputs: {prompt: inspect}
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: approved}}]
`
	for _, test := range []struct {
		name       string
		task       string
		evalSchema string
		filters    []string
		wantErr    string
		alsoErr    string
	}{
		{name: "invalid task", task: "id: first\nname: first\ninputs: {prompt: inspect}\nmcp_mocks: []\n", wantErr: "mcp_mocks must not be empty"},
		{name: "invalid task filter", task: task, filters: []string{"--task", "["}, wantErr: `invalid task filter pattern "["`},
		{name: "invalid tag filter", task: "tags: [selected]\n" + task, filters: []string{"--tags", "["}, wantErr: `invalid tag filter pattern "["`},
		{name: "invalid input schema", task: strings.Replace(task, "        responses:", "        input_schema: {type: 42}\n        responses:", 1), wantErr: `tool "ask_user" input schema`},
		{name: "external input schema", task: strings.Replace(task, "        responses:", "        input_schema: {$ref: 'https://never-read.invalid/schema'}\n        responses:", 1), wantErr: "external schema references are unavailable offline"},
		{name: "file input schema reference", task: strings.Replace(task, "        responses:", "        input_schema: {$ref: 'file:///never-read-admission-schema.json'}\n        responses:", 1), wantErr: "external schema references are unavailable offline"},
		{name: "replaced eval input schema", task: task, evalSchema: "        input_schema: {type: 42}\n", wantErr: `tool "ask_user" input schema`},
		{name: "invalid task and eval input schema", task: "id: first\nname: first\ninputs: {prompt: inspect}\nmcp_mocks: []\n", evalSchema: "        input_schema: {type: 42}\n", wantErr: "mcp_mocks must not be empty", alsoErr: `tool "ask_user" input schema`},
		{name: "invalid filter and eval input schema", task: task, filters: []string{"--task", "["}, evalSchema: "        input_schema: {type: 42}\n", wantErr: `invalid task filter pattern "["`, alsoErr: `tool "ask_user" input schema`},
		{name: "default input schema", task: task},
		{name: "null input schema", task: strings.Replace(task, "        responses:", "        input_schema: null\n        responses:", 1)},
		{name: "empty input schema", task: strings.Replace(task, "        responses:", "        input_schema: {}\n        responses:", 1)},
		{name: "local input schema reference", task: strings.Replace(task, "        responses:", "        input_schema: {type: object, $defs: {answer: {type: string}}, properties: {answer: {$ref: '#/$defs/answer'}}}\n        responses:", 1), filters: []string{"--task", "fir*"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetRunGlobals()
			transport := &preflightTransportSpy{}
			oldTransport := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = oldTransport })
			engine := &taskAdmissionStartupEngine{}
			constructions := 0
			previous := newRunEngine
			t.Cleanup(func() { newRunEngine = previous })
			newRunEngine = func(models.Config) (execution.AgentEngine, error) {
				constructions++
				return engine, nil
			}
			dir := t.TempDir()
			eval := `schemaVersion: "1.1"
name: admission
skill: example
config: {executor: copilot-sdk, model: mock, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
mcp_mocks:
  - name: host
    tools:
      ask_user:
` + test.evalSchema + `        responses: [{return: {answer: declined}}]
`
			path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(test.task), 0o600))
			cmd := newRunCommand()
			cmd.SetArgs(append([]string{path, "--context-dir", dir, "--output", output}, test.filters...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			require.Zero(t, transport.calls.Load(), "admission must not fetch schema references")
			require.NoFileExists(t, output)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				if test.alsoErr != "" {
					require.ErrorContains(t, err, "MCP mock admission")
					require.ErrorContains(t, err, test.alsoErr)
				}
				require.Zero(t, constructions)
				require.Zero(t, engine.starts)
				return
			}
			require.ErrorContains(t, err, "task admission startup sentinel")
			require.Equal(t, 1, constructions)
			require.Equal(t, 1, engine.starts)
		})
	}
}

// The CLI starts a copilot-sdk client before RunBenchmark, so it admits eval
// MCP mocks itself. A missing eval fixture must block the run only when a
// selected task still uses that mock.
func TestRunCommandAdmitsEvalMCPMocksAgainstSelectedTasks(t *testing.T) {
	const replaceTask = `id: %[1]s
name: %[1]s
inputs: {prompt: inspect}
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_schema: {}, return: {answer: Publish this version}}]
`
	for _, test := range []struct {
		name     string
		tasks    map[string]string
		filters  []string
		admitted bool
	}{
		{name: "every task replaces the missing fixture", tasks: map[string]string{"a.yaml": fmt.Sprintf(replaceTask, "a"), "b.yaml": fmt.Sprintf(replaceTask, "b")}, admitted: true},
		{name: "a task inherits the missing fixture", tasks: map[string]string{"a.yaml": fmt.Sprintf(replaceTask, "a"), "inherit.yaml": "id: inherit\nname: inherit\ninputs: {prompt: inspect}\n"}},
		{name: "the inheriting task is filtered out", tasks: map[string]string{"a.yaml": fmt.Sprintf(replaceTask, "a"), "inherit.yaml": "id: inherit\nname: inherit\ninputs: {prompt: inspect}\n"}, filters: []string{"--task", "a"}, admitted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetRunGlobals()
			dir := t.TempDir()
			t.Setenv("COPILOT_CLI_PATH", filepath.Join(dir, "must-not-launch"))
			eval := `schemaVersion: "1.1"
name: admission
skill: example
config: {executor: copilot-sdk, model: mock, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [tasks/*.yaml]
mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o700))
			for name, body := range test.tasks {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", name), []byte(body), 0o600))
			}
			path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
			cmd := newRunCommand()
			cmd.SetArgs(append([]string{path, "--context-dir", dir, "--output", output}, test.filters...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			require.Error(t, err, "the Copilot runtime is deliberately unavailable")
			require.NoFileExists(t, output)
			if test.admitted {
				require.NotContains(t, err.Error(), "MCP mock admission")
				require.ErrorContains(t, err, "failed to initialize agent", "admission passed, so the run reached client startup")
				return
			}
			require.ErrorContains(t, err, "MCP mock admission")
			require.ErrorContains(t, err, `resolve MCP mock "host"`)
			require.NotContains(t, err.Error(), "failed to initialize agent", "admission fails before client startup")
		})
	}
}
