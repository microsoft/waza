package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

type recoveryEvidenceEngine struct {
	execution.AgentEngine
	events    []copilot.SessionEvent
	workspace string
}

func (e *recoveryEvidenceEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return &execution.ExecutionResponse{
		Success: true, ModelID: "mock", SessionID: "recovery",
		Events: copilotevents.FromSDK(e.events), WorkspaceDir: e.workspace,
		WorkspaceFiles: map[string][]byte{"report.txt": []byte("Recovered after one transient error.")},
	}, nil
}

func TestRecoveryExampleGradesSDKEventsThroughRunBenchmark(t *testing.T) {
	evalPath := testutil.RepoFile(t, "examples", "scenarios", "recovery", "mcp", "eval.yaml")
	for _, errorForm := range []string{"result", "error", "both"} {
		t.Run(errorForm, func(t *testing.T) {
			spec, err := models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			spec.Tasks = []string{"tasks/transient.yaml"}
			first := &copilot.ToolExecutionCompleteData{ToolCallID: "first", Success: false}
			if errorForm != "result" {
				first.Error = &copilot.ToolExecutionCompleteError{Message: "temporary dependency error"}
				if errorForm == "both" {
					first.Result = &copilot.ToolExecutionCompleteResult{}
				}
			} else {
				first.Result = &copilot.ToolExecutionCompleteResult{Content: "temporary dependency error"}
			}
			workspace := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("Recovered after one transient error."), 0600))
			engine := &recoveryEvidenceEngine{
				AgentEngine: execution.NewMockEngine("mock"),
				workspace:   workspace,
				events: []copilot.SessionEvent{
					{Data: &copilot.ToolExecutionStartData{ToolCallID: "first", ToolName: "dependency-request", Arguments: map[string]any{"operation": "transient"}}},
					{Data: first},
					{Data: &copilot.ToolExecutionStartData{ToolCallID: "second", ToolName: "dependency-request", Arguments: map[string]any{"operation": "transient"}}},
					{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "second", Success: true, Result: &copilot.ToolExecutionCompleteResult{Content: `{"status":"recovered"}`}}},
					{Data: &copilot.ToolExecutionStartData{ToolCallID: "report", ToolName: "create", Arguments: map[string]any{"path": "report.txt"}}},
					{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "report", Success: true}},
				},
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(filepath.Dir(evalPath))), engine)
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			task := outcome.TestOutcomes[0]
			require.Equal(t, models.StatusPassed, task.Status, "%+v", task.Runs)
			require.NotEmpty(t, task.Runs)
			for _, run := range task.Runs {
				require.Len(t, run.ToolEvents, 3)
				require.Equal(t, "dependency-request", run.ToolEvents[0].ToolName)
				require.Contains(t, run.ToolEvents[0].Error, "temporary dependency error")
				require.True(t, run.Validations["observed-recovery"].Passed)
				require.True(t, run.Validations["recovery-report"].Passed)
			}
		})
	}
}

type admissionMutationEngine struct {
	execution.AgentEngine
	fixture string
	calls   int
}

func (e *admissionMutationEngine) Initialize(ctx context.Context) error {
	if err := os.WriteFile(e.fixture, []byte(`{"responses":[{"sequence":[]}]}`), 0600); err != nil {
		return err
	}
	return e.AgentEngine.Initialize(ctx)
}

func (e *admissionMutationEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	return e.AgentEngine.Execute(ctx, req)
}

func TestRunBenchmarkRejectsFixtureChangedAfterInitialAdmission(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
	fixture := filepath.Join(dir, "mocks", "request.json")
	require.NoError(t, os.WriteFile(fixture, []byte(`{"responses":[{"sequence":[{"return":"ready"}]}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: probe\nname: Probe\ninputs: {prompt: inspect}\n"), 0600))
	spec := &models.EvalSpec{
		SchemaVersion: models.ScenarioSchemaVersion,
		Scenario:      "recovery",
		SpecIdentity:  models.SpecIdentity{Name: "Admission mutation"},
		Config:        models.Config{EngineType: "mock", ModelID: "mock", TrialsPerTask: 1, TimeoutSec: 10},
		Tasks:         []string{"task.yaml"},
		MCPMocks:      []models.MCPMockConfig{{Name: "dependency", Fixtures: "mocks"}},
	}
	engine := &admissionMutationEngine{AgentEngine: execution.NewMockEngine("mock"), fixture: fixture}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, models.StatusFailed, outcome.TestOutcomes[0].Status)
	require.NotEmpty(t, outcome.TestOutcomes[0].Runs)
	require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Runs[0].Status)
	require.Contains(t, outcome.TestOutcomes[0].Runs[0].ErrorMsg, "resolve MCP mock")
	require.Zero(t, engine.calls, "a changed invalid source must not be warned away before execution")
}

func TestRunBenchmarkRejectsExternalMCPBeforeEngineInitialization(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "request.json"), []byte(`{"responses":[{"sequence":[]}]}`), 0600))
	cfg := config.NewEvalConfig(&models.EvalSpec{
		SchemaVersion: models.ScenarioSchemaVersion,
		Scenario:      "recovery",
		MCPMocks:      []models.MCPMockConfig{{Name: "dependency", Fixtures: "mocks"}},
	}, config.WithSpecDir(dir))
	runner := NewEvalRunner(cfg, nil)
	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "MCP mock admission")
	require.Nil(t, outcome)
}

func TestScenarioExecutionRequest(t *testing.T) {
	dir := t.TempDir()
	specDir := filepath.Join(dir, "evals")
	fixtures := filepath.Join(specDir, "fixtures")
	require.NoError(t, os.MkdirAll(fixtures, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "inventory.txt"), []byte("apples: 2\npears: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "instructions.md"), []byte("Use only sanitized inventory."), 0o600))
	t.Chdir(dir)
	require.NoError(t, os.WriteFile("SKILL.md", []byte("---\nname: ambient\ndescription: Ambient\n---\nDo unrelated work."), 0o600))

	for _, tt := range []struct {
		name      string
		skill     string
		evalPaths []string
		taskPaths []string
		disabled  []string
		want      bool
	}{
		{name: "no-ambient", want: true},
		{name: "whitespace-target", skill: " \t ", want: true},
		{name: "explicit-eval", evalPaths: []string{"../"}, want: false},
		{name: "last-eval-path-disabled", evalPaths: []string{"skills/foo"}, disabled: []string{"foo"}, want: true},
		{name: "empty-task", evalPaths: []string{"../"}, taskPaths: []string{}, want: true},
		{name: "explicit-task", taskPaths: []string{"../"}, want: false},
		{name: "no-skills-override", taskPaths: []string{"../"}, disabled: []string{"*"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := &models.EvalSpec{Scenario: "inventory", SkillName: tt.skill, SchemaVersion: "2.0", Config: models.Config{
				SkillPaths: tt.evalPaths, DisabledSkills: tt.disabled, InstructionFiles: []string{"instructions.md"},
			}}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(specDir), config.WithFixtureDir(fixtures)), nil)
			task := &models.TestCase{SkillPaths: tt.taskPaths, Stimulus: models.TaskStimulus{
				Message: "Read inventory.txt.", Metadata: map[string]any{"fixture": "fixtures/inventory.txt"},
			}}
			req, err := runner.buildExecutionRequest(task)
			require.NoError(t, err)
			require.Equal(t, tt.want, req.NoSkills)
			if tt.want {
				require.Nil(t, req.ToolPolicy)
			}
			if tt.name == "last-eval-path-disabled" {
				require.Empty(t, req.SkillPaths)
			}
			require.Equal(t, tt.skill, req.SkillName)
			require.NotEmpty(t, req.Resources)
			require.Equal(t, "inventory.txt", req.Resources[0].Path)
			require.Equal(t, []byte("apples: 2\npears: 1\n"), req.Resources[0].Content)
			require.NotEmpty(t, req.Instructions)
			if tt.taskPaths != nil && len(tt.taskPaths) == 0 {
				require.Empty(t, req.SkillPaths)
			}
			if len(tt.taskPaths) > 0 {
				require.Equal(t, []string{filepath.Clean(dir)}, req.SkillPaths)
			}
		})
	}
}

func TestRunBenchmarkRecordsEffectiveDependencyModes(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "inherit.yaml"), []byte(`id: inherit
name: Inherited mocks
inputs:
  prompt: Check the configured dependencies.
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "empty.yaml"), []byte(`id: empty
name: Empty override
inputs:
  prompt: Check the configured dependencies.
command_mocks: []
`), 0o600))

	spec := &models.EvalSpec{
		SchemaVersion: "2.0",
		Scenario:      "recovery",
		Tasks:         []string{"tasks/*.yaml"},
		CommandMocks:  []models.CommandMockConfig{{Name: "az", Responses: []models.CommandMockResponse{{Args: []string{"account"}, Stdout: "ok"}}}},
		MCPMocks: []models.MCPMockConfig{
			{Name: " replaced ", Tools: map[string]models.MCPMockTool{"ping": {Responses: []models.MCPMockResponse{{Return: "ok"}}}}},
			{Name: "mock-only", Tools: map[string]models.MCPMockTool{"ping": {Responses: []models.MCPMockResponse{{Return: "ok"}}}}},
		},
		Config: models.Config{
			EngineType:    "copilot-sdk",
			ModelID:       "mock-model",
			TrialsPerTask: 1,
			TimeoutSec:    30,
			ServerConfigs: map[string]any{
				"live":     map[string]any{"type": "http", "url": "https://example.invalid/mcp"},
				"replaced": map[string]any{"type": "stdio", "command": "server"},
			},
		},
	}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), execution.NewMockEngine("mock-model"), WithSkipGraders())
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 2)

	modesByTask := make(map[string][]models.DependencyMode, len(outcome.TestOutcomes))
	for _, task := range outcome.TestOutcomes {
		require.NotEmpty(t, task.Runs)
		modesByTask[task.TestID] = task.Runs[0].DependencyModes
	}
	require.Equal(t, []models.DependencyMode{
		{Kind: "cli", Name: "az", Mode: "mocked"},
		{Kind: "mcp", Name: "live", Mode: "live"},
		{Kind: "mcp", Name: "mock-only", Mode: "mocked"},
		{Kind: "mcp", Name: "replaced", Mode: "mocked"},
	}, modesByTask["inherit"])
	require.Equal(t, []models.DependencyMode{
		{Kind: "mcp", Name: "live", Mode: "live"},
		{Kind: "mcp", Name: "mock-only", Mode: "mocked"},
		{Kind: "mcp", Name: "replaced", Mode: "mocked"},
	}, modesByTask["empty"])
}

func TestRecoveryExamplesLoadThroughEvalAndTaskLoaders(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	for _, evalPath := range []string{
		"examples/scenarios/recovery/mcp/eval.yaml",
		"examples/scenarios/recovery/cli/eval.yaml",
	} {
		t.Run(evalPath, func(t *testing.T) {
			path := filepath.Join(root, evalPath)
			spec, err := models.LoadEvalSpec(path)
			require.NoError(t, err)
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(filepath.Dir(path))), nil)
			tasks, err := runner.loadTestCases()
			require.NoError(t, err)
			require.NotEmpty(t, tasks)
		})
	}
}

func TestScenarioRequiredSkillsUseEnabledPaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"foo", "bar"} {
		skillDir := filepath.Join(dir, "skills", name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Test skill\n---\n"), 0o600))
	}
	for _, tt := range []struct {
		name     string
		scenario string
		paths    []string
		disabled []string
		wantErr  string
	}{
		{name: "enabled", scenario: "inventory", paths: []string{"skills/foo"}},
		{name: "all-paths-filtered", scenario: "inventory", paths: []string{"skills/foo"}, disabled: []string{"foo"}, wantErr: "no skill_directories"},
		{name: "disabled-full-path", scenario: "inventory", paths: []string{"skills/foo"}, disabled: []string{"skills/foo"}, wantErr: "no skill_directories"},
		{name: "other-enabled-path", scenario: "inventory", paths: []string{"skills/foo", "skills/bar"}, disabled: []string{"foo"}, wantErr: "foo"},
		{name: "wildcard-disabled", scenario: "inventory", paths: []string{"skills/foo"}, disabled: []string{"*"}, wantErr: "no skill_directories"},
		{name: "none-marker", scenario: "inventory", paths: []string{"none"}, wantErr: "no skill_directories"},
		{name: "legacy-named-filter-preserved", paths: []string{"skills/foo"}, disabled: []string{"foo"}},
		{name: "legacy-wildcard-preserved", paths: []string{"skills/foo"}, disabled: []string{"*"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := &models.EvalSpec{Scenario: tt.scenario, Config: models.Config{
				SkillPaths: tt.paths, DisabledSkills: tt.disabled, RequiredSkills: []string{"foo"},
			}}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), nil)
			err := runner.validateRequiredSkills()
			offlineErr := ValidateRequiredSkillConfiguration(spec, dir)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorContains(t, offlineErr, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.NoError(t, offlineErr)
			}
		})
	}
}

type scenarioCountingEngine struct {
	execution.AgentEngine
	calls int
}

func (e *scenarioCountingEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	return e.AgentEngine.Execute(ctx, req)
}

func TestScenarioCacheBypassPreservesLegacyCaching(t *testing.T) {
	for _, scenario := range []string{"", "inventory"} {
		t.Run("scenario="+scenario, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte("id: inventory\nname: Inventory\ninputs:\n  prompt: Read inventory.\n  context:\n    fixture: inventory.txt\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "inventory.txt"), []byte("3"), 0o600))
			spec := &models.EvalSpec{Scenario: scenario, Tasks: []string{"task.yaml"}, Config: models.Config{
				TrialsPerTask: 1, TimeoutSec: 30, EngineType: "mock", ModelID: "mock",
			}}
			engine := &scenarioCountingEngine{AgentEngine: execution.NewMockEngine("mock")}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithCache(cache.New(t.TempDir())))
			first, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.False(t, first.TestOutcomes[0].Cached)
			data, err := json.Marshal(first)
			require.NoError(t, err)
			var emitted models.EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &emitted))
			require.Equal(t, models.CurrentResultSchemaVersion, first.SchemaVersion)
			require.Equal(t, models.CurrentResultSchemaVersion, emitted.SchemaVersion)
			require.Empty(t, emitted.SkillTested)
			if scenario != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "inventory.txt"), []byte("4"), 0o600))
				spec.MCPMocks = []models.MCPMockConfig{{Name: "inventory", Tools: map[string]models.MCPMockTool{
					"list": {Responses: []models.MCPMockResponse{{Return: "changed sanitized inventory"}}},
				}}}
			}
			second, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.NotNil(t, second.EvaluationUsage)
			if scenario == "" {
				require.True(t, second.TestOutcomes[0].Cached)
				require.Equal(t, 1, engine.calls)
				require.Empty(t, second.EvaluationUsage.Sessions, "cached diagnostics must not count as current billed attempts")
			} else {
				require.False(t, second.TestOutcomes[0].Cached)
				require.Equal(t, 2, engine.calls)
				require.Len(t, second.EvaluationUsage.Sessions, 1)
				require.Equal(t, second.TestOutcomes[0].Runs[0].SessionDigest.SessionID, second.EvaluationUsage.Sessions[0].SessionID)
			}
		})
	}
}

func TestScenarioMissingInstructionFails(t *testing.T) {
	spec := &models.EvalSpec{Scenario: "inventory", Config: models.Config{InstructionFiles: []string{"missing.md"}}}
	dir := t.TempDir()
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir), config.WithFixtureDir(dir)), nil)
	_, err := runner.buildExecutionRequest(&models.TestCase{Stimulus: models.TaskStimulus{Message: "hello"}})
	require.ErrorContains(t, err, "missing.md")
}

func TestScenarioOptionalAgentContext(t *testing.T) {
	for _, tt := range []struct {
		name, scenario string
		emptyOverride  bool
		skillPriority  bool
		malformed      bool
	}{
		{name: "legacy-agent"},
		{name: "scenario-agent", scenario: "inventory"},
		{name: "scenario-empty-override", scenario: "inventory", emptyOverride: true},
		{name: "scenario-skill-priority", scenario: "inventory", skillPriority: true},
		{name: "scenario-malformed-agent", scenario: "inventory", malformed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			body := "---\nname: reader\ntools: [fileRead]\n---\nRead only.\n"
			if tt.malformed {
				body = "---\nname: [unterminated\n---\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "reader.agent.md"), []byte(body), 0o600))
			if tt.skillPriority {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: reader\ndescription: Read only\n---\n"), 0o600))
			}
			spec := &models.EvalSpec{Scenario: tt.scenario, SkillName: "reader", Config: models.Config{SkillPaths: []string{dir}}}
			task := &models.TestCase{Stimulus: models.TaskStimulus{Message: "hello"}}
			if tt.emptyOverride {
				task.SkillPaths = []string{}
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), nil)
			req, err := runner.buildExecutionRequest(task)
			if tt.malformed {
				require.Error(t, err)
				require.Nil(t, req)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.emptyOverride, req.NoSkills)
			if tt.emptyOverride || tt.skillPriority {
				require.Nil(t, req.ToolPolicy)
			} else {
				require.NotNil(t, req.ToolPolicy)
				require.Equal(t, execution.ToolPolicyAllowList, req.ToolPolicy.Mode)
				require.True(t, req.ToolPolicy.IsAllowed("view"))
				require.False(t, req.ToolPolicy.IsAllowed("bash"))
			}
		})
	}
}
