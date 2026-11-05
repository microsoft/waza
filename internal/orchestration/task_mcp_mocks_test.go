package orchestration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// The eval answers every ask_user question with "Not now" and also exposes a
// docs server. One task replaces only the host mock; the other inherits both.
const taskMCPMockEval = `schemaVersion: "%s"
name: task-mcp-mocks
skill: example
config:
  executor: copilot-sdk
  model: test-model
  trials_per_task: 1
  timeout_seconds: 60
tasks:
  - tasks/*.yaml
graders: []
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            return: {answer: Not now, wasFreeform: false}
  - name: docs
    tools:
      read_doc:
        responses:
          - match_schema: {}
            return: {text: shared docs}
`

const taskMCPMockPublishTask = `id: publish
name: Publish
inputs:
  prompt: Publish the skill.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema:
              type: object
              required: [choices]
              properties:
                choices: {type: array, contains: {const: Publish this version}}
            return: {answer: Publish this version, wasFreeform: false}
          - match_schema: {}
            return: {answer: Not now, wasFreeform: false}
`

const taskMCPMockInheritTask = `id: inherit
name: Inherit
inputs:
  prompt: Publish the skill.
`

func writeTaskMCPMockEval(t *testing.T, schemaVersion string, tasks map[string]string) (*EvalRunner, string) {
	t.Helper()
	dir := t.TempDir()
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, fmt.Appendf(nil, taskMCPMockEval, schemaVersion), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	for name, body := range tasks {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", name), []byte(body), 0o600))
	}
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	return NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), nil), dir
}

func TestLoadTasks_TaskMCPMocksRequireSchemaVersionAndValidResponses(t *testing.T) {
	runner, _ := writeTaskMCPMockEval(t, "1.1", map[string]string{"publish.yaml": taskMCPMockPublishTask})
	// An eval with its own mcp_mocks cannot load at 1.0, so lower the version
	// after loading to check the task gate on its own.
	runner.cfg.Spec().SchemaVersion = "1.0"
	runner.cfg.Spec().MCPMocks = nil
	_, err := runner.loadTestCases()
	require.ErrorContains(t, err, "mcp_mocks requires schemaVersion 1.1 or newer")

	runner, _ = writeTaskMCPMockEval(t, "1.1", map[string]string{"bad.yaml": `id: bad
name: Bad
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    fixtures: missing-fixture-directory
`})
	_, err = runner.loadTestCases()
	require.NoError(t, err, "task fixtures are admitted only for selected active tasks, not at load")
	require.ErrorContains(t, AdmitEvalMCPMocks(runner.cfg, nil, nil), `task "bad": resolve MCP mock "host"`)

	runner, _ = writeTaskMCPMockEval(t, "1.1", map[string]string{"regex.yaml": `id: regex
name: Regex
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    fixtures: missing-fixture-directory
    tools:
      ask_user:
        responses:
          - match_regex: {question: "("}
            return: {answer: Not now}
`})
	_, err = runner.loadTestCases()
	require.ErrorContains(t, err, "MCP mock admission", "inline task declarations are still validated at load")
	require.ErrorContains(t, err, "invalid regex")

	runner, _ = writeTaskMCPMockEval(t, "1.1", map[string]string{"sequence.yaml": `id: sequence
name: Sequence
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            sequence:
              - return: {answer: Not now}
`})
	_, err = runner.loadTestCases()
	require.ErrorContains(t, err, "task fault sources")
}

func TestBuildExecutionRequest_TaskMCPMocksReplaceByName(t *testing.T) {
	runner, dir := writeTaskMCPMockEval(t, "1.1", map[string]string{
		"publish.yaml": taskMCPMockPublishTask,
		"inherit.yaml": taskMCPMockInheritTask,
	})
	tasks, err := runner.loadTestCases()
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	byID := map[string]*models.TestCase{}
	for _, task := range tasks {
		byID[task.TestID] = task
	}

	answers := map[string]string{}
	for id, task := range byID {
		req, err := runner.buildExecutionRequest(task)
		require.NoError(t, err)
		require.Len(t, req.MCPServers, 2, id)
		host := readMockConfigFile(t, stdioArgs(t, req, "host"))
		answers[id] = firstAnswer(t, host, "ask_user")
		docs := readMockConfigFile(t, stdioArgs(t, req, "docs"))
		require.Contains(t, docs.Tools, "read_doc", "%s keeps the eval docs mock", id)
		modes := configuredDependencyModes(req, models.EffectiveMCPMocks(runner.cfg.Spec(), task))
		require.Equal(t, []models.DependencyMode{
			{Kind: "mcp", Name: "docs", Mode: "mocked"},
			{Kind: "mcp", Name: "host", Mode: "mocked"},
		}, modes)
	}
	require.Equal(t, map[string]string{"publish": "Publish this version", "inherit": "Not now"}, answers)
	require.Len(t, runner.cfg.Spec().MCPMocks, 2, "the eval list is unchanged")
	require.Equal(t, map[string]any{"answer": "Not now", "wasFreeform": false}, runner.cfg.Spec().MCPMocks[0].Tools["ask_user"].Responses[0].Return)
	require.DirExists(t, dir)
}

func TestBuildExecutionRequest_TaskMCPFixturesResolveFromEvalDir(t *testing.T) {
	runner, dir := writeTaskMCPMockEval(t, "1.1", map[string]string{"fixture.yaml": `id: fixture
name: Fixture
inputs:
  prompt: hi
mcp_mocks:
  - name: host
    fixtures: mcp/host
`})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mcp", "host"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp", "host", "ask_user.json"),
		[]byte(`{"responses":[{"return":{"answer":"from eval-relative fixture"}}]}`), 0o600))
	tasks, err := runner.loadTestCases()
	require.NoError(t, err, "a task in tasks/ resolves fixtures from the eval directory")
	require.Len(t, tasks, 1)
	req, err := runner.buildExecutionRequest(tasks[0])
	require.NoError(t, err)
	require.Equal(t, "from eval-relative fixture", firstAnswer(t, readMockConfigFile(t, stdioArgs(t, req, "host")), "ask_user"))
	readMockConfigFile(t, stdioArgs(t, req, "docs"))
}

func TestControlledAndNativeGatesRejectTaskMCPMocks(t *testing.T) {
	task := &models.TestCase{TestID: "t", MCPMocks: []models.MCPMockConfig{{Name: "host"}}}
	controlled := &models.EvalSpec{Scenario: "s", Config: models.Config{EngineType: "mock"}}
	require.ErrorContains(t, ValidateControlledConfiguration(controlled, &models.TestCase{TestID: "t"}), "at least one observable check", "without task mocks the dependency gate passes")
	require.ErrorContains(t, ValidateControlledConfiguration(controlled, task), "cannot certify")
	native := &models.EvalSpec{Scenario: "s", Config: models.Config{EngineType: "copilot-sdk", ModelID: "m"}}
	require.ErrorContains(t, ValidateNativeSnapshotConfiguration(native, task), "unsupported native dependency")
}

// TestTaskMCPMocksServeOverTheRealSubprocess loads task files through the
// runner, writes each task's mock configuration and asks the actual waza
// __mcp-mock server the same question once per task.
func TestTaskMCPMocksServeOverTheRealSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the waza binary")
	}
	binary := filepath.Join(t.TempDir(), "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	buildStarted := time.Now()
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/waza")
	output, err := build.CombinedOutput()
	buildElapsed := time.Since(buildStarted)
	require.NoError(t, err, "MCP mock CLI build_elapsed=%s: %s", buildElapsed, output)
	t.Logf("MCP mock production CLI build_elapsed=%s", buildElapsed)

	runner, _ := writeTaskMCPMockEval(t, "1.1", map[string]string{
		"publish.yaml": taskMCPMockPublishTask,
		"inherit.yaml": taskMCPMockInheritTask,
	})
	tasks, err := runner.loadTestCases()
	require.NoError(t, err)
	question := map[string]any{"question": "Publish now?", "choices": []string{"Publish this version", "Not now"}}
	answers := map[string]string{}
	for _, task := range tasks {
		req, err := runner.buildExecutionRequest(task)
		require.NoError(t, err)
		for name := range req.MCPServers {
			args := stdioArgs(t, req, name)
			t.Cleanup(func() { _ = os.Remove(args[2]) })
		}
		answers[task.TestID] = callMockTool(t, binary, stdioArgs(t, req, "host"), "ask_user", question)
	}
	require.Equal(t, `{"answer":"Publish this version","wasFreeform":false}`, answers["publish"])
	require.Equal(t, `{"answer":"Not now","wasFreeform":false}`, answers["inherit"])
}

func TestMockToolStderrHelperProcess(t *testing.T) {
	mode := os.Getenv("WAZA_TEST_MOCK_STDERR")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for range 2 {
		if !scanner.Scan() {
			os.Exit(2)
		}
	}
	_, err := fmt.Fprintln(os.Stderr, "mock diagnostic sentinel")
	require.NoError(t, err)
	if mode == "error" {
		require.NoError(t, os.Stdout.Close())
		_, err := io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)
		os.Exit(3)
	}
	if mode == "malformed" {
		_, err := fmt.Fprintln(os.Stdout, "not-json")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)
		os.Exit(3)
	}
	if mode == "cancel" {
		_, err := fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)
		os.Exit(3)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(os.Stderr, strings.Repeat("mock stderr output\n", 65536))
		done <- err
	}()
	_, err = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"text":"ready"}]}}`)
	require.NoError(t, err)
	require.NoError(t, <-done)
}

func TestMockToolDiagnosticCallerHelperProcess(t *testing.T) {
	mode := os.Getenv("WAZA_TEST_MOCK_STDERR")
	if mode == "" {
		return
	}
	args := []string{"-test.run=^TestMockToolStderrHelperProcess$"}
	if mode == "start-cancel" {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		callMockToolWithContext(t, ctx, os.Args[0], args, "probe", nil, nil)
		t.Fatal("canceled mock subprocess unexpectedly started")
	}
	if mode == "cancel" {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		callMockToolWithContext(t, ctx, os.Args[0], args, "probe", nil, func(id json.RawMessage) {
			if string(id) == "1" {
				cancel()
			}
		})
		t.Fatal("canceled mock subprocess unexpectedly returned")
	}
	answer := callMockTool(t, os.Args[0], args, "probe", nil)
	require.Equal(t, "ready", answer)
}

func TestMockToolDiagnosticsJoinStderrBeforeReading(t *testing.T) {
	for _, mode := range []string{"success", "error", "malformed", "cancel", "start-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMockToolDiagnosticCallerHelperProcess$")
			cmd.Env = append(os.Environ(), "WAZA_TEST_MOCK_STDERR="+mode)
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "helper must join without reaching its deadline: %s", output)
			require.NotContains(t, string(output), "DATA RACE")
			if mode == "success" {
				require.NoError(t, err, "%s", output)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "%s", output)
				require.Equal(t, 1, exit.ExitCode(), "expected caller assertion failure, not crash: %s", output)
				switch mode {
				case "error":
					require.Contains(t, string(output), "reading mock response: EOF")
					require.Contains(t, string(output), "joined wait:")
					require.Regexp(t, `setup_elapsed=\S+ process_start_elapsed=\S+ post_start_ctx_err=<nil> protocol_elapsed=\S+ pre_cleanup_ctx_err=<nil> response_frames=0`, string(output))
				case "malformed":
					require.Contains(t, string(output), "invalid character")
					require.Contains(t, string(output), "joined mock subprocess cleanup:")
					require.Regexp(t, `setup_elapsed=\S+ process_start_elapsed=\S+ post_start_ctx_err=<nil> protocol_elapsed=\S+ pre_cleanup_ctx_err=<nil> response_frames=1`, string(output))
				case "cancel":
					require.Contains(t, string(output), "reading mock response:")
					require.Regexp(t, `setup_elapsed=\S+ process_start_elapsed=\S+ post_start_ctx_err=<nil> protocol_elapsed=\S+ pre_cleanup_ctx_err=context canceled response_frames=1`, string(output))
				case "start-cancel":
					require.Regexp(t, `setup_elapsed=\S+ process_start_elapsed=\S+ post_start_ctx_err=context canceled`, string(output))
					require.NotContains(t, string(output), "mock diagnostic sentinel")
					return
				}
				require.Contains(t, string(output), "mock diagnostic sentinel")
			}
		})
	}
}

func callMockTool(t *testing.T, binary string, args []string, tool string, arguments map[string]any) string {
	t.Helper()
	return callMockToolWithContext(t, t.Context(), binary, args, tool, arguments, nil)
}

func callMockToolWithContext(t *testing.T, parent context.Context, binary string, args []string, tool string, arguments map[string]any, onResponse func(json.RawMessage)) string {
	t.Helper()
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, args...)
	child.WaitDelay = 2 * time.Second
	child.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1")
	stdin, err := child.StdinPipe()
	require.NoError(t, err)
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	child.Stderr = &stderr
	setupElapsed := time.Since(started)
	startedProcess := time.Now()
	err = child.Start()
	processStartElapsed, postStartContextErr := time.Since(startedProcess), ctx.Err()
	require.NoError(t, err, "setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v",
		setupElapsed, processStartElapsed, postStartContextErr)
	protocolStarted := time.Now()
	waited := false
	defer func() {
		cancel()
		if !waited {
			closeErr := errors.Join(stdin.Close(), stdout.Close())
			waitErr := child.Wait()
			require.NoError(t, closeErr, "closing mock subprocess pipes during cleanup")
			if waitErr != nil {
				t.Logf("joined mock subprocess cleanup: %v; stderr: %s", waitErr, stderr.String())
			}
		}
	}()
	params, err := json.Marshal(map[string]any{"name": tool, "arguments": arguments})
	require.NoError(t, err)
	_, err = fmt.Fprintf(stdin, "{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}\n{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"id\":2,\"params\":%s}\n", params)
	require.NoError(t, err, "setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v protocol_elapsed=%s pre_cleanup_ctx_err=%v response_frames=0",
		setupElapsed, processStartElapsed, postStartContextErr, time.Since(protocolStarted), ctx.Err())
	reader := bufio.NewReader(stdout)
	var text string
	responseFrames := 0
	for text == "" {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			protocolElapsed, preCleanupContextErr := time.Since(protocolStarted), ctx.Err()
			cancel()
			closeErr := errors.Join(stdin.Close(), stdout.Close())
			waitErr := child.Wait()
			waited = true
			t.Fatalf("reading mock response: %v; closing pipes: %v; joined wait: %v; setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v protocol_elapsed=%s pre_cleanup_ctx_err=%v response_frames=%d; stderr: %s",
				err, closeErr, waitErr, setupElapsed, processStartElapsed, postStartContextErr,
				protocolElapsed, preCleanupContextErr, responseFrames, stderr.String())
		}
		responseFrames++
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(line, &response),
			"setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v protocol_elapsed=%s pre_cleanup_ctx_err=%v response_frames=%d",
			setupElapsed, processStartElapsed, postStartContextErr, time.Since(protocolStarted), ctx.Err(), responseFrames)
		if onResponse != nil {
			onResponse(response.ID)
		}
		if string(response.ID) == "2" {
			require.False(t, response.Result.IsError, "%s", line)
			require.Len(t, response.Result.Content, 1)
			text = response.Result.Content[0].Text
		}
	}
	protocolElapsed := time.Since(protocolStarted)
	shutdownStarted := time.Now()
	require.NoError(t, stdin.Close())
	_, _ = io.Copy(io.Discard, reader)
	waitErr := child.Wait()
	waited = true
	shutdownElapsed := time.Since(shutdownStarted)
	require.NoError(t, waitErr, "setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v protocol_elapsed=%s shutdown_elapsed=%s pre_cleanup_ctx_err=%v response_frames=%d; stderr: %s",
		setupElapsed, processStartElapsed, postStartContextErr, protocolElapsed, shutdownElapsed, ctx.Err(), responseFrames, stderr.String())
	t.Logf("MCP mock process setup_elapsed=%s process_start_elapsed=%s post_start_ctx_err=%v protocol_elapsed=%s response_frames=%d shutdown_elapsed=%s",
		setupElapsed, processStartElapsed, postStartContextErr, protocolElapsed, responseFrames, shutdownElapsed)
	return text
}

type writtenMockConfig struct {
	Tools map[string]struct {
		Responses []struct {
			Return json.RawMessage `json:"return"`
		} `json:"responses"`
	} `json:"tools"`
}

func firstAnswer(t *testing.T, cfg writtenMockConfig, tool string) string {
	t.Helper()
	require.NotEmpty(t, cfg.Tools[tool].Responses)
	var value struct {
		Answer string `json:"answer"`
	}
	require.NoError(t, json.Unmarshal(cfg.Tools[tool].Responses[0].Return, &value))
	return value.Answer
}

func readMockConfigFile(t *testing.T, args []string) writtenMockConfig {
	t.Helper()
	require.Len(t, args, 3)
	require.Equal(t, []string{"__mcp-mock", "--config-file"}, args[:2])
	t.Cleanup(func() { _ = os.Remove(args[2]) })
	data, err := os.ReadFile(args[2])
	require.NoError(t, err)
	var cfg writtenMockConfig
	require.NoError(t, json.Unmarshal(data, &cfg))
	return cfg
}

func stdioArgs(t *testing.T, req *execution.ExecutionRequest, name string) []string {
	t.Helper()
	server, ok := req.MCPServers[name].(copilot.MCPStdioServerConfig)
	require.True(t, ok, "%s is a stdio mock server", name)
	return server.Args
}
