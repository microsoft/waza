package orchestration

import (
	"bufio"
	"context"
	"encoding/json"
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
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/waza")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)

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

func callMockTool(t *testing.T, binary string, args []string, tool string, arguments map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, args...)
	child.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1")
	stdin, err := child.StdinPipe()
	require.NoError(t, err)
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	child.Stderr = &stderr
	require.NoError(t, child.Start())
	params, err := json.Marshal(map[string]any{"name": tool, "arguments": arguments})
	require.NoError(t, err)
	_, err = fmt.Fprintf(stdin, "{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}\n{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"id\":2,\"params\":%s}\n", params)
	require.NoError(t, err)
	reader := bufio.NewReader(stdout)
	var text string
	for text == "" {
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err, "stderr: %s", stderr.String())
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(line, &response))
		if string(response.ID) == "2" {
			require.False(t, response.Result.IsError, "%s", line)
			require.Len(t, response.Result.Content, 1)
			text = response.Result.Content[0].Text
		}
	}
	require.NoError(t, stdin.Close())
	_, _ = io.Copy(io.Discard, reader)
	require.NoError(t, child.Wait(), "stderr: %s", stderr.String())
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
