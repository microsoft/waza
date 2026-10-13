package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

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
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
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
