package orchestration

import (
	"context"
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
		evalPaths []string
		taskPaths []string
		disabled  []string
		want      bool
	}{
		{name: "no-ambient", want: true},
		{name: "explicit-eval", evalPaths: []string{"../"}, want: false},
		{name: "empty-task", evalPaths: []string{"../"}, taskPaths: []string{}, want: true},
		{name: "explicit-task", taskPaths: []string{"../"}, want: false},
		{name: "no-skills-override", taskPaths: []string{"../"}, disabled: []string{"*"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := &models.EvalSpec{Scenario: "inventory", SchemaVersion: "2.0", Config: models.Config{
				SkillPaths: tt.evalPaths, DisabledSkills: tt.disabled, InstructionFiles: []string{"instructions.md"},
			}}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(specDir), config.WithFixtureDir(fixtures)), nil)
			task := &models.TestCase{SkillPaths: tt.taskPaths, Stimulus: models.TaskStimulus{
				Message: "Read inventory.txt.", Metadata: map[string]any{"fixture": "fixtures/inventory.txt"},
			}}
			req, err := runner.buildExecutionRequest(task)
			require.NoError(t, err)
			require.Equal(t, tt.want, req.NoSkills)
			require.Empty(t, req.SkillName)
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
			if scenario != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "inventory.txt"), []byte("4"), 0o600))
				spec.MCPMocks = []models.MCPMockConfig{{Name: "inventory", Tools: map[string]models.MCPMockTool{
					"list": {Responses: []models.MCPMockResponse{{Return: "changed sanitized inventory"}}},
				}}}
			}
			second, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			if scenario == "" {
				require.True(t, second.TestOutcomes[0].Cached)
				require.Equal(t, 1, engine.calls)
			} else {
				require.False(t, second.TestOutcomes[0].Cached)
				require.Equal(t, 2, engine.calls)
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
