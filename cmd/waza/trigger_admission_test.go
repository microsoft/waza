package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

const admissionTrigger = "skill: example\nshould_trigger_prompts:\n  - prompt: trigger probe\n"
const triggerAdmissionEval = `schemaVersion: "1.1"
name: trigger-admission
skill: example
config: {executor: copilot-sdk, model: test, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
    tools:
      ask_user:
        responses: [{return: {answer: eval}}]
`
const triggerAdmissionTask = `id: probe
name: Probe
inputs: {prompt: task probe}
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: task}}]
`

func TestRunCommandTriggerDependenciesAdmittedBeforeEngine(t *testing.T) {
	for _, mode := range []string{"absent", "valid", "generated", "malformed", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			resetRunGlobals()
			dir := t.TempDir()
			path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
			require.NoError(t, os.WriteFile(path, []byte(triggerAdmissionEval), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(triggerAdmissionTask), 0o600))
			triggerPath := filepath.Join(dir, "trigger_tests.yaml")
			switch mode {
			case "valid":
				require.NoError(t, os.WriteFile(triggerPath, []byte(admissionTrigger), 0o600))
			case "generated":
				staged := filepath.Join(dir, "staged-trigger.yaml")
				require.NoError(t, os.WriteFile(staged, []byte(admissionTrigger), 0o600))
				eval := triggerAdmissionEval + "hooks:\n  before_run:\n    - command: '" +
					cliHookCommand(t, "copy", staged, triggerPath) + "'\n      error_on_fail: true\n"
				require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
				require.NoFileExists(t, triggerPath)
			case "malformed":
				require.NoError(t, os.WriteFile(triggerPath, []byte("skill: ["), 0o600))
			case "unreadable":
				require.NoError(t, os.Mkdir(triggerPath, 0o700))
			}
			previous := newRunEngine
			t.Cleanup(func() { newRunEngine = previous })
			engine := &taskAdmissionStartupEngine{}
			constructions := 0
			newRunEngine = func(models.Config) (execution.AgentEngine, error) {
				constructions++
				return engine, nil
			}
			cmd := newRunCommand()
			cmd.SetArgs([]string{path, "--context-dir", dir, "--output", output, "--skip-graders", "--no-skills"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			require.NoFileExists(t, output)
			if mode == "absent" {
				require.ErrorContains(t, err, "task admission startup sentinel")
				require.Equal(t, 1, constructions, "replaced missing eval fixtures are valid without a trigger phase")
				return
			}
			require.Zero(t, constructions)
			require.ErrorContains(t, err, "MCP mock admission")
			require.ErrorContains(t, err, "missing-host-fixtures")
			if mode == "malformed" || mode == "unreadable" {
				require.ErrorContains(t, err, "loading trigger tests", "trigger errors do not hide independent mock failures")
			}
		})
	}
}

type triggerConfigCapture struct {
	*execution.MockEngine
	mu       sync.Mutex
	answers  []string
	prompts  []string
	readErrs []error
}

func (e *triggerConfigCapture) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	cfg, ok := req.MCPServers["host"].(copilot.MCPStdioServerConfig)
	if !ok || len(cfg.Args) == 0 {
		return nil, fmt.Errorf("host mock requires stdio configuration with a fixture argument: %T", req.MCPServers["host"])
	}
	data, err := os.ReadFile(cfg.Args[len(cfg.Args)-1])
	var mock mcpmock.Config
	if err == nil {
		err = json.Unmarshal(data, &mock)
	}
	var answer string
	if err == nil {
		value, ok := mock.Tools["ask_user"].Responses[0].Return.(map[string]any)
		if !ok {
			err = fmt.Errorf("ask_user response must be an object")
		} else if answer, ok = value["answer"].(string); !ok {
			err = fmt.Errorf("ask_user answer must be a string")
		}
	}
	e.mu.Lock()
	e.readErrs = append(e.readErrs, err)
	e.prompts = append(e.prompts, req.Message)
	if err == nil {
		e.answers = append(e.answers, answer)
	}
	e.mu.Unlock()
	resp := &execution.ExecutionResponse{Success: true, FinalOutput: "ready"}
	if req.Message == "trigger probe" {
		resp.SkillInvocations = []execution.SkillInvocation{{Name: "example"}}
	}
	return resp, err
}

func TestRunCommandTriggerUsesEvalMocksAndSavesResultsAcrossCacheHit(t *testing.T) {
	dir := t.TempDir()
	path, output := filepath.Join(dir, "eval.yaml"), filepath.Join(dir, "results.json")
	eval := strings.Replace(triggerAdmissionEval, "    fixtures: missing-host-fixtures\n", "", 1)
	require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(triggerAdmissionTask), 0o600))
	triggerPath := filepath.Join(dir, "trigger_tests.yaml")
	require.NoError(t, os.WriteFile(triggerPath, []byte(admissionTrigger), 0o600))
	previous := newRunEngine
	t.Cleanup(func() { newRunEngine = previous })
	for run := range 2 {
		resetRunGlobals()
		engine := &triggerConfigCapture{MockEngine: execution.NewMockEngine("test")}
		newRunEngine = func(models.Config) (execution.AgentEngine, error) {
			// Discovery must use the exact spec admitted before engine creation.
			require.NoError(t, os.WriteFile(triggerPath, []byte("skill: ["), 0o600))
			return engine, nil
		}
		cmd := newRunCommand()
		cmd.SetArgs([]string{path, "--context-dir", dir, "--output", output,
			"--skip-graders", "--no-skills", "--cache", "--cache-dir", filepath.Join(dir, "cache")})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		require.NoError(t, cmd.Execute())
		for _, err := range engine.readErrs {
			require.NoError(t, err)
		}
		if run == 0 {
			require.Equal(t, []string{"task", "eval"}, engine.answers)
			require.Equal(t, []string{"task probe", "trigger probe"}, engine.prompts)
		} else {
			require.Equal(t, []string{"eval"}, engine.answers, "a cached task must not skip the trigger phase")
			require.Equal(t, []string{"trigger probe"}, engine.prompts)
		}
		data, err := os.ReadFile(output)
		require.NoError(t, err)
		var outcome models.EvaluationOutcome
		require.NoError(t, json.Unmarshal(data, &outcome))
		require.Len(t, outcome.TriggerResults, 1)
		require.Equal(t, "trigger probe", outcome.TriggerResults[0].Prompt)
		require.True(t, outcome.TriggerResults[0].DidTrigger)
		require.NotNil(t, outcome.TriggerMetrics)
		require.NoError(t, os.WriteFile(triggerPath, []byte(admissionTrigger), 0o600))
	}
}
