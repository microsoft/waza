package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunHookHelperProcess is not a test on its own: hook commands re-invoke
// the test binary with this test selected and an action after "--".
func TestRunHookHelperProcess(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "append":
		f, err := os.OpenFile(args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(args[2] + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	case "copy":
		data, err := os.ReadFile(args[1])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(args[2], data, 0o600))
	default:
		t.Fatalf("unknown hook helper action %q", args[0])
	}
}

func cliHookCommand(t *testing.T, action string, args ...string) string {
	t.Helper()
	parts := append([]string{os.Args[0], "-test.run=^TestRunHookHelperProcess$", "--", action}, args...)
	for _, part := range parts {
		if strings.ContainsAny(part, " \t") {
			t.Skipf("hook command argument %q contains whitespace", part)
		}
	}
	return strings.Join(parts, " ")
}

// The CLI admits MCP mocks and starts the engine before RunBenchmark, so it
// must run before_run first, once, and hand that setup to the runner.
func TestRunCommandRunsBeforeRunBeforeLoadingTasks(t *testing.T) {
	resetRunGlobals()
	dir := t.TempDir()
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(dir, "must-not-launch"))
	staged := filepath.Join(dir, "staged-prompt.md")
	require.NoError(t, os.WriteFile(staged, []byte("Inspect the workspace.\n"), 0o600))
	log := filepath.Join(dir, "log.txt")
	eval := `schemaVersion: "1.1"
name: hook-generated
skill: example
config: {executor: mock, model: mock, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
hooks:
  before_run:
    - command: '` + cliHookCommand(t, "copy", staged, filepath.Join(dir, "prompt.md")) + `'
      error_on_fail: true
    - command: '` + cliHookCommand(t, "append", log, "before_run") + `'
      error_on_fail: true
  after_run:
    - command: '` + cliHookCommand(t, "append", log, "after_run") + `'
      error_on_fail: true
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{match_schema: {}, return: {answer: Not now}}]
`
	path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: probe\nname: Probe\ninputs: {prompt_file: prompt.md}\n"), 0o600))
	require.NoFileExists(t, filepath.Join(dir, "prompt.md"), "before_run generates the prompt file")

	cmd := newRunCommand()
	cmd.SetArgs([]string{path, "--context-dir", dir, "--output", output})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())
	require.FileExists(t, output)

	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "before_run\nafter_run\n", strings.ReplaceAll(string(data), "\r\n", "\n"), "setup and teardown each run exactly once")
}
