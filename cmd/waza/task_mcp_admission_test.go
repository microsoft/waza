package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
