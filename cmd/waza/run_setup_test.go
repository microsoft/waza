package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
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
	case "sleep":
		require.NoError(t, os.WriteFile(args[1], []byte("started"), 0o600))
		time.Sleep(2 * time.Minute)
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

// blockingInitEngine blocks in Initialize until its context is canceled.
type blockingInitEngine struct {
	execution.AgentEngine
	started chan struct{}
	release chan struct{}
}

func (e *blockingInitEngine) Initialize(ctx context.Context) error {
	close(e.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return nil
	}
}

// writeInterruptSuite writes a mock-executor eval whose hooks log to hooks.log.
// extraBeforeRun is appended to before_run.
func writeInterruptSuite(t *testing.T, dir, extraBeforeRun string) (string, string) {
	t.Helper()
	log := filepath.Join(dir, "hooks.log")
	eval := `schemaVersion: "1.1"
name: interrupt
skill: example
config: {executor: mock, model: mock, trials_per_task: 1, timeout_seconds: 30}
metrics: []
tasks: [task.yaml]
hooks:
  before_run:
    - command: '` + cliHookCommand(t, "append", log, "before_run") + `'
      error_on_fail: true
` + extraBeforeRun + `  after_run:
    - command: '` + cliHookCommand(t, "append", log, "after_run") + `'
      error_on_fail: true
`
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: probe\nname: Probe\ninputs: {prompt: inspect}\n"), 0o600))
	return path, log
}

func requireCLIHookLog(t *testing.T, log string) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "before_run\nafter_run\n", strings.ReplaceAll(string(data), "\r\n", "\n"), "after_run runs exactly once after an interrupt")
}

// executeUntil runs the command with a cancelable context, cancels it when
// ready is closed, and requires the command to return promptly.
func executeUntil(t *testing.T, args []string, ready <-chan struct{}) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newRunCommand()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	select {
	case <-ready:
	case err := <-done:
		require.FailNow(t, "run finished before the interrupt point", "err=%v", err)
	case <-time.After(30 * time.Second):
		require.FailNow(t, "run never reached the interrupt point")
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		require.FailNow(t, "run did not stop promptly after cancellation")
		return nil
	}
}

func TestRunCommandInterruptDuringBeforeRunStopsBeforeEngine(t *testing.T) {
	resetRunGlobals()
	dir := t.TempDir()
	marker := filepath.Join(dir, "sleep-started")
	path, log := writeInterruptSuite(t, dir, "    - command: '"+cliHookCommand(t, "sleep", marker)+"'\n")
	constructed := 0
	previous := newRunEngine
	t.Cleanup(func() { newRunEngine = previous })
	newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
		constructed++
		return previous(cfg)
	}
	ready := make(chan struct{})
	go func() {
		for {
			if _, err := os.Stat(marker); err == nil {
				close(ready)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	err := executeUntil(t, []string{path, "--context-dir", dir, "--output", filepath.Join(dir, "results.json")}, ready)
	require.ErrorContains(t, err, "context canceled")
	require.Zero(t, constructed, "no engine is created after an interrupt during setup")
	requireCLIHookLog(t, log)
}

func TestRunCommandInterruptDuringEngineInitStopsPromptly(t *testing.T) {
	resetRunGlobals()
	dir := t.TempDir()
	path, log := writeInterruptSuite(t, dir, "")
	engine := &blockingInitEngine{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(engine.release) })
	previous := newRunEngine
	t.Cleanup(func() { newRunEngine = previous })
	newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
		inner, err := previous(cfg)
		engine.AgentEngine = inner
		return engine, err
	}
	err := executeUntil(t, []string{path, "--context-dir", dir, "--output", filepath.Join(dir, "results.json")}, engine.started)
	require.ErrorContains(t, err, "failed to initialize agent")
	require.ErrorContains(t, err, "context canceled")
	requireCLIHookLog(t, log)
}

type blockedStartupClient struct {
	execution.CopilotClient
	entered chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (c *blockedStartupClient) Start(ctx context.Context) error {
	close(c.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return errors.New("test cleanup released startup")
	}
}

func (c *blockedStartupClient) Stop() error {
	close(c.stopped)
	return nil
}

func TestRunCommandInterruptDuringActualCopilotStartupRunsAfterRunOnce(t *testing.T) {
	resetRunGlobals()
	dir := t.TempDir()
	path, log := writeInterruptSuite(t, dir, "")
	client := &blockedStartupClient{entered: make(chan struct{}), stopped: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(client.release) })
	previous := newRunEngine
	t.Cleanup(func() { newRunEngine = previous })
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		return execution.NewCopilotEngineBuilder("test", &execution.CopilotEngineBuilderOptions{
			NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
		}).Build(), nil
	}
	output := filepath.Join(dir, "results.json")
	err := executeUntil(t, []string{path, "--context-dir", dir, "--output", output}, client.entered)
	require.ErrorContains(t, err, "failed to initialize agent")
	require.ErrorContains(t, err, "context canceled")
	select {
	case <-client.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("partial startup was not cleaned up")
	}
	require.NoFileExists(t, output)
	requireCLIHookLog(t, log)
}
