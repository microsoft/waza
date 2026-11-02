package orchestration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// TestRunHookHelperProcess is not a test on its own: hook commands re-invoke
// the test binary with this test selected and an action after "--".
func TestRunHookHelperProcess(t *testing.T) {
	args := hookHelperArgs()
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

func hookHelperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// hookCommand returns a hook command that runs a helper action. Hook commands
// are split on whitespace, so paths containing it cannot be passed.
func hookCommand(t *testing.T, action string, args ...string) string {
	t.Helper()
	parts := append([]string{os.Args[0], "-test.run=^TestRunHookHelperProcess$", "--", action}, args...)
	for _, part := range parts {
		if strings.ContainsAny(part, " \t") {
			t.Skipf("hook command argument %q contains whitespace", part)
		}
	}
	return strings.Join(parts, " ")
}

// writeHookSuite writes an eval whose before_run hook generates the only task
// file from a staged copy outside the task glob. The hooks log to log.txt.
func writeHookSuite(t *testing.T, mcpMocks, task string) (*EvalRunner, *admissionEngine, string) {
	t.Helper()
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged-task.yaml")
	require.NoError(t, os.WriteFile(staged, []byte(task), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	log := filepath.Join(dir, "log.txt")
	eval := `schemaVersion: "1.1"
name: hook-generated
skill: example
config:
  executor: copilot-sdk
  model: test-model
  trials_per_task: 1
  timeout_seconds: 60
tasks:
  - tasks/*.yaml
hooks:
  before_run:
    - command: '` + hookCommand(t, "copy", staged, filepath.Join(dir, "tasks", "generated.yaml")) + `'
      error_on_fail: true
    - command: '` + hookCommand(t, "append", log, "before_run") + `'
      error_on_fail: true
  after_run:
    - command: '` + hookCommand(t, "append", log, "after_run") + `'
      error_on_fail: true
` + mcpMocks
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(eval), 0o600))
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	engine := &admissionEngine{t: t, answers: map[string]string{}, servers: map[string][]string{}}
	return NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()), engine, log
}

func requireHookLog(t *testing.T, log string) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "before_run\nafter_run\n", strings.ReplaceAll(string(data), "\r\n", "\n"), "setup and teardown each run exactly once")
}

const hookHostMock = `mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            return: {answer: Not now}
`

func TestRunBenchmark_BeforeRunGeneratesTaskBeforeLoading(t *testing.T) {
	runner, engine, log := writeHookSuite(t, hookHostMock, admissionInheritTask)
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "the task file only exists after before_run")
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "Not now", engine.answers["inherit"])
	require.Equal(t, 1, engine.initialized)
	requireHookLog(t, log)
}

func TestRunBenchmark_AdmissionSeesHookGeneratedTaskOverride(t *testing.T) {
	runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`, admissionReplaceTask("generated"))
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "the generated task replaces host, so its missing fixtures are unused")
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "Publish this version", engine.answers["generated"])
	requireHookLog(t, log)
}

func TestRunBenchmark_AdmissionFailureAfterSetupStillTearsDown(t *testing.T) {
	runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`, admissionInheritTask)
	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "MCP mock admission")
	require.ErrorContains(t, err, `resolve MCP mock "host"`)
	require.Nil(t, outcome)
	require.Zero(t, engine.initialized, "admission still fails before engine initialization")
	requireHookLog(t, log)
}

// A failed pass tears down its setup; retrying RunBenchmark on the same runner
// must run before_run again, so inputs removed by after_run are regenerated.
func TestRunBenchmark_RetryAfterFailedStartRunsSetupAgain(t *testing.T) {
	t.Run("admission failure", func(t *testing.T) {
		runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: mcp-host
`, admissionInheritTask)
		_, err := runner.RunBenchmark(t.Context())
		require.ErrorContains(t, err, "MCP mock admission")
		require.Zero(t, engine.initialized)

		fixtures := filepath.Join(filepath.Dir(log), "mcp-host")
		require.NoError(t, os.MkdirAll(fixtures, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(fixtures, "ask_user.json"), []byte(`{"responses":[{"return":{"answer":"Not now"}}]}`), 0o600))
		outcome, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
		require.Len(t, outcome.TestOutcomes, 1)
		requireHookLogLines(t, log, "before_run", "after_run", "before_run", "after_run")
	})
	t.Run("engine initialization failure", func(t *testing.T) {
		runner, engine, log := writeHookSuite(t, hookHostMock, admissionInheritTask)
		engine.failInits = 1
		_, err := runner.RunBenchmark(t.Context())
		require.ErrorContains(t, err, "engine unavailable")
		outcome, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
		require.Len(t, outcome.TestOutcomes, 1)
		require.Equal(t, 2, engine.initialized)
		requireHookLogLines(t, log, "before_run", "after_run", "before_run", "after_run")
	})
}

func requireHookLogLines(t *testing.T, log string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, strings.Join(want, "\n")+"\n", strings.ReplaceAll(string(data), "\r\n", "\n"))
}
