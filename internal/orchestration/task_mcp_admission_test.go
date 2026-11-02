package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// admissionEngine records how often it is initialized and which host answer
// and MCP servers each task received.
type admissionEngine struct {
	t           *testing.T
	mu          sync.Mutex
	initialized int
	answers     map[string]string
	servers     map[string][]string
}

func (e *admissionEngine) Initialize(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.initialized++
	return nil
}
func (e *admissionEngine) Shutdown(context.Context) error         { return nil }
func (e *admissionEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *admissionEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	args := stdioArgs(e.t, req, "host")
	data, err := os.ReadFile(args[len(args)-1])
	require.NoError(e.t, err)
	var cfg writtenMockConfig
	require.NoError(e.t, json.Unmarshal(data, &cfg))
	var names []string
	for name := range req.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	e.mu.Lock()
	e.answers[req.TaskName] = firstAnswer(e.t, cfg, "ask_user")
	e.servers[req.TaskName] = names
	e.mu.Unlock()
	return &execution.ExecutionResponse{FinalOutput: "done", SessionID: "session", WorkspaceDir: "workspace", Success: true}, nil
}

const admissionInheritTask = `id: inherit
name: inherit
inputs:
  prompt: Publish the skill.
`

func admissionReplaceTask(id string) string {
	return "id: " + id + "\nname: " + id + `
inputs:
  prompt: Publish the skill.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            return: {answer: Publish this version, wasFreeform: false}
`
}

// writeAdmissionSuite writes an eval whose "host" mock is hostMock and whose
// "docs" mock is valid, and returns a runner over it.
func writeAdmissionSuite(t *testing.T, hostMock string, tasks map[string]string, options ...RunnerOption) (*EvalRunner, *admissionEngine) {
	t.Helper()
	dir := t.TempDir()
	eval := `schemaVersion: "1.1"
name: admission
skill: example
config:
  executor: copilot-sdk
  model: test-model
  trials_per_task: 1
  timeout_seconds: 60
tasks:
  - tasks/*.yaml
mcp_mocks:
` + hostMock + `
  - name: docs
    tools:
      read_doc:
        responses:
          - match_schema: {}
            return: {text: shared docs}
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(eval), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	for name, body := range tasks {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", name), []byte(body), 0o600))
	}
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	engine := &admissionEngine{t: t, answers: map[string]string{}, servers: map[string][]string{}}
	options = append(options, WithSkipGraders())
	return NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, options...), engine
}

const missingFixtureHost = `  - name: host
    fixtures: missing-host-fixtures`

func TestRunBenchmark_TaskMCPAdmissionRejectsBeforeStartup(t *testing.T) {
	for _, test := range []struct {
		name    string
		task    string
		options []RunnerOption
		wantErr string
	}{
		{name: "invalid task", task: "id: first\nname: first\ninputs: {prompt: inspect}\nmcp_mocks: []\n", wantErr: "mcp_mocks must not be empty"},
		{name: "invalid task filter", task: admissionReplaceTask("first"), options: []RunnerOption{WithTaskFilters("[")}, wantErr: `invalid task filter pattern "["`},
		{name: "invalid tag filter", task: "tags: [selected]\n" + admissionReplaceTask("first"), options: []RunnerOption{WithTagFilters("[")}, wantErr: `invalid tag filter pattern "["`},
		{name: "invalid input schema", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: {type: 42}\n        responses:", 1), wantErr: `tool "ask_user" input schema`},
		{name: "external input schema", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: {$ref: 'https://never-read.invalid/schema'}\n        responses:", 1), wantErr: "external schema references are unavailable offline"},
		{name: "file input schema reference", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: {$ref: 'file:///never-read-admission-schema.json'}\n        responses:", 1), wantErr: "external schema references are unavailable offline"},
		{name: "default input schema", task: admissionReplaceTask("first")},
		{name: "null input schema", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: null\n        responses:", 1)},
		{name: "empty input schema", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: {}\n        responses:", 1)},
		{name: "local input schema reference", task: strings.Replace(admissionReplaceTask("first"), "        responses:", "        input_schema: {type: object, $defs: {answer: {type: string}}, properties: {answer: {$ref: '#/$defs/answer'}}}\n        responses:", 1), options: []RunnerOption{WithTaskFilters("fir*")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, engine := writeAdmissionSuite(t, `  - name: host
    tools:
      ask_user:
        responses: [{return: {answer: Not now}}]`, map[string]string{"first.yaml": test.task}, test.options...)
			outcome, err := runner.RunBenchmark(t.Context())
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Nil(t, outcome)
				require.Zero(t, engine.initialized, "invalid configuration must not start an engine")
				require.Empty(t, engine.answers)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, engine.initialized)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Equal(t, "Publish this version", engine.answers["first"])
		})
	}
}

func TestRunBenchmark_ReplacedEvalMockInputSchemaIsValidated(t *testing.T) {
	runner, engine := writeAdmissionSuite(t, missingFixtureHost+`
    tools:
      ask_user:
        input_schema: {type: 42}
        responses: [{return: {answer: Not now}}]`, map[string]string{"first.yaml": admissionReplaceTask("first")})
	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, `tool "ask_user" input schema`)
	require.NotContains(t, err.Error(), "missing-host-fixtures")
	require.Nil(t, outcome)
	require.Zero(t, engine.initialized)
}

func TestRunBenchmark_TaskSelectionErrorRetainsEvalMockErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		task    string
		options []RunnerOption
		wantErr string
	}{
		{name: "invalid task", task: "id: first\nname: first\ninputs: {prompt: inspect}\nmcp_mocks: []\n", wantErr: "mcp_mocks must not be empty"},
		{name: "invalid filter", task: admissionReplaceTask("first"), options: []RunnerOption{WithTaskFilters("[")}, wantErr: `invalid task filter pattern "["`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, engine := writeAdmissionSuite(t, missingFixtureHost+`
    tools:
      ask_user:
        input_schema: {type: 42}
        responses: [{return: {answer: Not now}}]`, map[string]string{"first.yaml": test.task}, test.options...)
			outcome, err := runner.RunBenchmark(t.Context())
			require.ErrorContains(t, err, test.wantErr)
			require.ErrorContains(t, err, "MCP mock admission")
			require.ErrorContains(t, err, `tool "ask_user" input schema`)
			require.ErrorContains(t, err, "missing-host-fixtures")
			require.Nil(t, outcome)
			require.Zero(t, engine.initialized)
			require.Empty(t, engine.answers)
		})
	}
}

func TestRunBenchmark_EvalMockReplacedByEveryTaskSkipsFixtureAdmission(t *testing.T) {
	runner, engine := writeAdmissionSuite(t, missingFixtureHost, map[string]string{
		"first.yaml":  admissionReplaceTask("first"),
		"second.yaml": admissionReplaceTask("second"),
	})
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "an eval mock that every task replaces is never started")
	require.Len(t, outcome.TestOutcomes, 2)
	require.Equal(t, 1, engine.initialized)
	for _, task := range []string{"first", "second"} {
		require.Equal(t, "Publish this version", engine.answers[task], task)
		require.Equal(t, []string{"docs", "host"}, engine.servers[task], "the unreplaced eval docs mock still applies to %s", task)
	}
}

func TestRunBenchmark_EvalMockFixturesAdmittedWhenATaskUsesThem(t *testing.T) {
	runner, engine := writeAdmissionSuite(t, missingFixtureHost, map[string]string{
		"first.yaml":   admissionReplaceTask("first"),
		"inherit.yaml": admissionInheritTask,
	})
	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "MCP mock admission")
	require.ErrorContains(t, err, `resolve MCP mock "host"`)
	require.Nil(t, outcome)
	require.Zero(t, engine.initialized, "admission still fails before engine initialization")
}

func TestRunBenchmark_TaskFilterSelectsTasksForAdmission(t *testing.T) {
	runner, engine := writeAdmissionSuite(t, missingFixtureHost, map[string]string{
		"first.yaml":   admissionReplaceTask("first"),
		"inherit.yaml": admissionInheritTask,
	}, WithTaskFilters("first"))
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "the inheriting task is not selected, so the missing fixture is unused")
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "Publish this version", engine.answers["first"])
}

func TestRunBenchmark_ReplacedEvalMockInlineDeclarationsAreValidated(t *testing.T) {
	runner, engine := writeAdmissionSuite(t, missingFixtureHost+`
    tools:
      ask_user:
        responses:
          - match_regex: {question: "("}
            return: {answer: Not now}`, map[string]string{
		"first.yaml":  admissionReplaceTask("first"),
		"second.yaml": admissionReplaceTask("second"),
	})
	_, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "MCP mock admission")
	require.ErrorContains(t, err, "invalid regex")
	require.NotContains(t, err.Error(), "missing-host-fixtures", "the replaced mock's fixtures are not read")
	require.Zero(t, engine.initialized)
}

func missingFixtureTask(id string, enabled bool) string {
	return "id: " + id + "\nname: " + id + "\nenabled: " + map[bool]string{true: "true", false: "false"}[enabled] + `
inputs:
  prompt: Publish the skill.
mcp_mocks:
  - name: extra
    fixtures: missing-task-fixtures
`
}

func TestRunBenchmark_TaskMockFixturesAdmittedOnlyForSelectedActiveTasks(t *testing.T) {
	validHost := `  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            return: {answer: Not now}`
	for _, test := range []struct {
		name    string
		tasks   map[string]string
		options []RunnerOption
		wantErr string
	}{
		{name: "disabled task", tasks: map[string]string{"first.yaml": admissionReplaceTask("first"), "off.yaml": missingFixtureTask("off", false)}},
		{name: "filtered-out task", tasks: map[string]string{"first.yaml": admissionReplaceTask("first"), "other.yaml": missingFixtureTask("other", true)}, options: []RunnerOption{WithTaskFilters("first")}},
		{name: "selected active task", tasks: map[string]string{"first.yaml": admissionReplaceTask("first"), "other.yaml": missingFixtureTask("other", true)}, wantErr: `task "other": resolve MCP mock "extra"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, engine := writeAdmissionSuite(t, validHost, test.tasks, test.options...)
			outcome, err := runner.RunBenchmark(t.Context())
			if test.wantErr != "" {
				require.ErrorContains(t, err, "MCP mock admission")
				require.ErrorContains(t, err, test.wantErr)
				require.Nil(t, outcome)
				require.Zero(t, engine.initialized, "admission fails before engine initialization")
				return
			}
			require.NoError(t, err, "an unselected task's missing fixtures must not abort the run")
			require.Len(t, outcome.TestOutcomes, 1)
			require.Equal(t, "Publish this version", engine.answers["first"])
		})
	}
}
