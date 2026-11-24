package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputePassRate tests the pass rate calculation helper
func TestComputePassRate(t *testing.T) {
	tests := []struct {
		name         string
		runs         []models.RunResult
		expectedRate float64
	}{
		{
			name: "all pass",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusPassed},
			},
			expectedRate: 1.0,
		},
		{
			name: "partial pass",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedRate: 0.667,
		},
		{
			name: "all fail",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedRate: 0.0,
		},
		{
			name: "single trial pass",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
			},
			expectedRate: 1.0,
		},
		{
			name: "single trial fail",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
			},
			expectedRate: 0.0,
		},
		{
			name:         "zero trials guard",
			runs:         []models.RunResult{},
			expectedRate: 0.0,
		},
		{
			name: "error status counts as fail",
			runs: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusError},
				{RunNumber: 3, Status: models.StatusPassed},
			},
			expectedRate: 0.667,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := &models.TestOutcome{
				Runs: tt.runs,
			}
			rate := computePassRate(outcome)
			assert.InDelta(t, tt.expectedRate, rate, 0.001)
		})
	}
}

// TestComputeSkillImpact tests the skill impact calculation
func TestComputeSkillImpact(t *testing.T) {
	tests := []struct {
		name               string
		withSkillsRuns     []models.RunResult
		withoutRuns        []models.RunResult
		expectedDelta      float64
		expectedPercentMin float64
		expectedPercentMax float64
	}{
		{
			name: "skills improve both pass",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedDelta:      0.333,
			expectedPercentMin: 90,
			expectedPercentMax: 110,
		},
		{
			name: "skills hurt",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedDelta:      -0.333,
			expectedPercentMin: -60,
			expectedPercentMax: -40,
		},
		{
			name: "both fail zero delta",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedDelta:      0.0,
			expectedPercentMin: -10,
			expectedPercentMax: 10,
		},
		{
			name: "baseline zero division guard",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
			},
			expectedDelta:      0.667,
			expectedPercentMin: 6600,
			expectedPercentMax: 6700,
		},
		{
			name: "perfect scores both",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusPassed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusPassed},
			},
			expectedDelta:      0.0,
			expectedPercentMin: -10,
			expectedPercentMax: 10,
		},
		{
			name: "single trial improvement",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusFailed},
			},
			expectedDelta:      1.0,
			expectedPercentMin: 9900,
			expectedPercentMax: 10100,
		},
		{
			name: "baseline better than skills",
			withSkillsRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusFailed},
				{RunNumber: 3, Status: models.StatusFailed},
				{RunNumber: 4, Status: models.StatusFailed},
				{RunNumber: 5, Status: models.StatusFailed},
			},
			withoutRuns: []models.RunResult{
				{RunNumber: 1, Status: models.StatusPassed},
				{RunNumber: 2, Status: models.StatusPassed},
				{RunNumber: 3, Status: models.StatusPassed},
				{RunNumber: 4, Status: models.StatusFailed},
				{RunNumber: 5, Status: models.StatusFailed},
			},
			expectedDelta:      -0.4,
			expectedPercentMin: -70,
			expectedPercentMax: -60,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSkills := &models.TestOutcome{
				TestID: "test-001",
				Runs:   tt.withSkillsRuns,
			}
			without := &models.TestOutcome{
				TestID: "test-001",
				Runs:   tt.withoutRuns,
			}

			impact := computeSkillImpact(withSkills, without)

			require.NotNil(t, impact)
			assert.InDelta(t, tt.expectedDelta, impact.Delta, 0.001, "delta mismatch")
			assert.GreaterOrEqual(t, impact.PercentChange, tt.expectedPercentMin, "percent change too low")
			assert.LessOrEqual(t, impact.PercentChange, tt.expectedPercentMax, "percent change too high")

			expectedWithRate := computePassRate(withSkills)
			expectedWithoutRate := computePassRate(without)

			assert.InDelta(t, expectedWithRate, impact.PassRateWithSkills, 0.001)
			assert.InDelta(t, expectedWithoutRate, impact.PassRateBaseline, 0.001)
		})
	}
}

// TestRunBenchmark_BaselineNoSkills tests that baseline mode warns and runs single pass when no skills configured
// NOTE: This test is written against the design spec and will need task loading setup once implementation lands
func TestRunBenchmark_BaselineNoSkills(t *testing.T) {
	t.Skip("Requires baseline implementation and task loading setup")

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Baseline:  true,
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 1,
			TimeoutSec:    60,
		},
		Tasks: []string{"task-001.yaml"},
	}

	cfg := config.NewEvalConfig(spec)
	engine := execution.NewMockEngine("gpt-4")
	runner := NewEvalRunner(cfg, engine)

	ctx := context.Background()
	outcome, err := runner.RunBenchmark(ctx)

	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.False(t, outcome.IsBaseline, "should not run baseline comparison when no skills configured")
	assert.Nil(t, outcome.BaselineOutcome, "baseline outcome should be nil")

	assert.Len(t, outcome.TestOutcomes, 1)
}

// TestRunBenchmark_BaselineWithSkills tests baseline mode executes two passes with skills configured
// NOTE: This test is written against the design spec and will need task loading setup once implementation lands
func TestRunBenchmark_BaselineWithSkills(t *testing.T) {
	t.Skip("Requires baseline implementation and task loading setup")

	tmpSkillDir := t.TempDir()

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Baseline:  true,
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 1,
			TimeoutSec:    60,
			SkillPaths:    []string{tmpSkillDir},
		},
		Tasks: []string{"task-001.yaml"},
	}

	cfg := config.NewEvalConfig(spec)
	engine := execution.NewMockEngine("gpt-4")
	runner := NewEvalRunner(cfg, engine)

	ctx := context.Background()
	outcome, err := runner.RunBenchmark(ctx)

	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.True(t, outcome.IsBaseline, "should run baseline comparison with skills configured")
	require.NotNil(t, outcome.BaselineOutcome, "baseline outcome should be present")

	assert.Len(t, outcome.TestOutcomes, 1)
	assert.Len(t, outcome.BaselineOutcome.TestOutcomes, 1)

	testOutcome := outcome.TestOutcomes[0]
	require.NotNil(t, testOutcome.SkillImpact, "skill impact should be computed")
	assert.Equal(t, "task-001", testOutcome.TestID)
}

// TestRunBenchmark_BaselineEmptyTasks tests baseline mode with no tasks (edge case)
// NOTE: This test is written against the design spec and will need task loading setup once implementation lands
func TestRunBenchmark_BaselineEmptyTasks(t *testing.T) {
	t.Skip("Requires baseline implementation and task loading setup")

	tmpSkillDir := t.TempDir()

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Baseline:  true,
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 1,
			TimeoutSec:    60,
			SkillPaths:    []string{tmpSkillDir},
		},
		Tasks: []string{},
	}

	cfg := config.NewEvalConfig(spec)
	engine := execution.NewMockEngine("gpt-4")
	runner := NewEvalRunner(cfg, engine)

	ctx := context.Background()
	outcome, err := runner.RunBenchmark(ctx)

	require.NoError(t, err)
	require.NotNil(t, outcome)

	assert.True(t, outcome.IsBaseline)
	assert.Len(t, outcome.TestOutcomes, 0)
}

// TestMergeBaselineOutcomes_TaskMismatch tests error handling when task sets don't align
func TestMergeBaselineOutcomes_TaskMismatch(t *testing.T) {
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 1,
			TimeoutSec:    60,
		},
	}

	cfg := config.NewEvalConfig(spec)
	runner := NewEvalRunner(cfg, nil)

	withSkills := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{TestID: "task-001", DisplayName: "Test 1"},
			{TestID: "task-002", DisplayName: "Test 2"},
		},
	}

	withoutSkills := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{TestID: "task-001", DisplayName: "Test 1"},
		},
	}

	_, err := runner.mergeBaselineOutcomes(withSkills, withoutSkills)

	require.Error(t, err, "should error on task mismatch")
	assert.Contains(t, err.Error(), "baseline mismatch", "error should mention baseline mismatch")
	assert.Contains(t, err.Error(), "task-002", "error should mention missing task")
}

// TestMergeBaselineOutcomes_ExtraTaskInBaseline tests error when baseline has tasks not in skills-enabled
func TestMergeBaselineOutcomes_ExtraTaskInBaseline(t *testing.T) {
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 1,
			TimeoutSec:    60,
		},
	}

	cfg := config.NewEvalConfig(spec)
	runner := NewEvalRunner(cfg, nil)

	withSkills := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{TestID: "task-001", DisplayName: "Test 1"},
		},
	}

	withoutSkills := &models.EvaluationOutcome{
		TestOutcomes: []models.TestOutcome{
			{TestID: "task-001", DisplayName: "Test 1"},
			{TestID: "task-002", DisplayName: "Test 2"},
		},
	}

	_, err := runner.mergeBaselineOutcomes(withSkills, withoutSkills)

	require.Error(t, err, "should error on extra task in baseline")
	assert.Contains(t, err.Error(), "baseline mismatch", "error should mention baseline mismatch")
	assert.Contains(t, err.Error(), "task-002", "error should mention extra task")
}

// TestMergeBaselineOutcomes_Success tests successful outcome merging
func TestMergeBaselineOutcomes_Success(t *testing.T) {
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-eval",
		},
		SkillName: "test-skill",
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "gpt-4",
			TrialsPerTask: 3,
			TimeoutSec:    60,
		},
	}

	cfg := config.NewEvalConfig(spec)
	runner := NewEvalRunner(cfg, nil)

	withSkills := &models.EvaluationOutcome{
		RunID:       "eval-001",
		SkillTested: "test-skill",
		BenchName:   "test-eval",
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "task-001",
				DisplayName: "Test 1",
				Runs: []models.RunResult{
					{RunNumber: 1, Status: models.StatusPassed},
					{RunNumber: 2, Status: models.StatusPassed},
					{RunNumber: 3, Status: models.StatusFailed},
				},
			},
			{
				TestID:      "task-002",
				DisplayName: "Test 2",
				Runs: []models.RunResult{
					{RunNumber: 1, Status: models.StatusPassed},
					{RunNumber: 2, Status: models.StatusPassed},
					{RunNumber: 3, Status: models.StatusPassed},
				},
			},
		},
	}

	withoutSkills := &models.EvaluationOutcome{
		RunID:       "eval-001-baseline",
		SkillTested: "test-skill",
		BenchName:   "test-eval (baseline)",
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "task-001",
				DisplayName: "Test 1",
				Runs: []models.RunResult{
					{RunNumber: 1, Status: models.StatusPassed},
					{RunNumber: 2, Status: models.StatusFailed},
					{RunNumber: 3, Status: models.StatusFailed},
				},
			},
			{
				TestID:      "task-002",
				DisplayName: "Test 2",
				Runs: []models.RunResult{
					{RunNumber: 1, Status: models.StatusPassed},
					{RunNumber: 2, Status: models.StatusFailed},
					{RunNumber: 3, Status: models.StatusFailed},
				},
			},
		},
	}

	merged, err := runner.mergeBaselineOutcomes(withSkills, withoutSkills)

	require.NoError(t, err)
	require.NotNil(t, merged)

	assert.True(t, merged.IsBaseline)
	assert.Equal(t, withoutSkills, merged.BaselineOutcome)

	require.Len(t, merged.TestOutcomes, 2)

	task1 := merged.TestOutcomes[0]
	require.NotNil(t, task1.SkillImpact)
	assert.InDelta(t, 0.667, task1.SkillImpact.PassRateWithSkills, 0.001)
	assert.InDelta(t, 0.333, task1.SkillImpact.PassRateBaseline, 0.001)
	assert.InDelta(t, 0.334, task1.SkillImpact.Delta, 0.001)

	task2 := merged.TestOutcomes[1]
	require.NotNil(t, task2.SkillImpact)
	assert.InDelta(t, 1.0, task2.SkillImpact.PassRateWithSkills, 0.001)
	assert.InDelta(t, 0.333, task2.SkillImpact.PassRateBaseline, 0.001)
	assert.InDelta(t, 0.667, task2.SkillImpact.Delta, 0.001)
}

func TestRunBenchmark_BaselineDisablesAllSkills(t *testing.T) {
	for _, tt := range []struct {
		name       string
		evalPaths  []string
		taskPaths  string
		wantTreats func(dir string) []string
	}{
		{name: "eval-skills", evalPaths: []string{"eval-skills"}, wantTreats: func(dir string) []string {
			return []string{filepath.Join(dir, "eval-skills")}
		}},
		{name: "task-only-skills", taskPaths: "skill_directories: [task-skills]\n", wantTreats: func(dir string) []string {
			return []string{filepath.Join(dir, "task-skills")}
		}},
		{name: "task-override", evalPaths: []string{"eval-skills"}, taskPaths: "skill_directories: [task-skills]\n", wantTreats: func(dir string) []string {
			return []string{filepath.Join(dir, "task-skills")}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			// An ambient agent definition in the working directory must not leak into the baseline.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ambient.agent.md"), []byte("---\nname: ambient\ntools: [view]\n---\nRead only.\n"), 0o600))
			task := "id: task-001\nname: Task 1\ninputs:\n  prompt: hello\n" + tt.taskPaths
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0o600))

			evalPaths := tt.evalPaths
			spec := &models.EvalSpec{
				SpecIdentity: models.SpecIdentity{Name: "baseline-eval"},
				SkillName:    "ambient",
				Baseline:     true,
				Tasks:        []string{"task.yaml"},
				Config: models.Config{
					EngineType: "mock", ModelID: "mock", TrialsPerTask: 1, TimeoutSec: 30,
					SkillPaths: evalPaths,
				},
			}
			engine := &captureCacheEngine{MockEngine: execution.NewMockEngine("mock")}
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)

			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.True(t, outcome.IsBaseline, "task-only skills must not skip the baseline comparison")
			require.NotNil(t, outcome.BaselineOutcome)
			require.Len(t, engine.requests, 2)

			treatment, baseline := engine.requests[0], engine.requests[1]
			require.False(t, treatment.NoSkills)
			require.Equal(t, tt.wantTreats(dir), treatment.SkillPaths)
			require.NotNil(t, treatment.ToolPolicy, "treatment keeps ambient agent discovery")

			require.True(t, baseline.NoSkills, "baseline must explicitly disable skills")
			require.Nil(t, baseline.ToolPolicy, "baseline must not resolve ambient agent definitions")

			require.Equal(t, evalPaths, spec.Config.SkillPaths, "original skill configuration is restored")
		})
	}
}

func TestRunBenchmark_BaselineIgnoresUnselectedTaskSkills(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.yaml"), []byte("id: plain\nname: Plain\ninputs:\n  prompt: hello\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skilled.yaml"), []byte("id: skilled\nname: Skilled\ninputs:\n  prompt: hello\nskill_directories: [task-skills]\n"), 0o600))
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "baseline-eval"},
		Baseline:     true,
		Tasks:        []string{"plain.yaml", "skilled.yaml"},
		Config:       models.Config{EngineType: "mock", ModelID: "mock", TrialsPerTask: 1, TimeoutSec: 30},
	}
	engine := &captureCacheEngine{MockEngine: execution.NewMockEngine("mock")}
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithTaskFilters("plain"))

	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.False(t, outcome.IsBaseline, "skills on unselected tasks must not start a baseline comparison")
	require.Len(t, engine.requests, 1)
}

type pairwiseCaptureEngine struct {
	*execution.MockEngine

	mu                 sync.Mutex
	initializeCalls    int
	baselineOutputs    []string
	treatmentOutputs   []string
	judgePrompts       []string
	actorNoSkills      []bool
	failBaseline       bool
	errorBaselineRetry bool
}

func (e *pairwiseCaptureEngine) Initialize(ctx context.Context) error {
	e.initializeCalls++
	return e.MockEngine.Initialize(ctx)
}

func (e *pairwiseCaptureEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if req.EphemeralSession {
		e.judgePrompts = append(e.judgePrompts, req.Message)
		winner := "B"
		if len(e.judgePrompts)%2 == 0 {
			winner = "A"
		}
		for _, tool := range req.Tools {
			if tool.Name == "set_pairwise_winner" {
				if _, err := tool.Handler(copilot.ToolInvocation{Arguments: map[string]any{
					"winner": winner, "magnitude": "much-better", "reasoning": "selected candidate",
				}}); err != nil {
					return nil, err
				}
			}
		}
		return &execution.ExecutionResponse{Success: true, ModelID: "mock", SessionID: "judge"}, nil
	}

	e.actorNoSkills = append(e.actorNoSkills, req.NoSkills)
	if req.NoSkills {
		e.baselineOutputs = append(e.baselineOutputs, "")
		if e.errorBaselineRetry && len(e.baselineOutputs) == 2 {
			return nil, fmt.Errorf("baseline retry execution failed")
		}
		if e.failBaseline {
			return &execution.ExecutionResponse{Success: false, ErrorMsg: "baseline unavailable"}, nil
		}
		switch len(e.baselineOutputs) {
		case 1:
			e.baselineOutputs[len(e.baselineOutputs)-1] = "baseline-stale"
		case 2:
			e.baselineOutputs[len(e.baselineOutputs)-1] = "baseline-1-final"
		default:
			e.baselineOutputs[len(e.baselineOutputs)-1] = "baseline-2-final"
		}
		return &execution.ExecutionResponse{
			Success: true, ExecutionSucceeded: true, ModelID: "mock",
			SessionID: "baseline", FinalOutput: e.baselineOutputs[len(e.baselineOutputs)-1],
		}, nil
	}

	e.treatmentOutputs = append(e.treatmentOutputs, fmt.Sprintf("candidate-%d-final", len(e.treatmentOutputs)+1))
	return &execution.ExecutionResponse{
		Success: true, ExecutionSucceeded: true, ModelID: "mock",
		SessionID: "treatment", FinalOutput: e.treatmentOutputs[len(e.treatmentOutputs)-1],
	}, nil
}

func pairwiseTestSpec(baseline bool) *models.EvalSpec {
	return &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "pairwise-eval"},
		Tasks:        []string{"task.yaml"},
		Baseline:     baseline,
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindPrompt,
				Identifier: "quality",
				Parameters: models.PromptGraderParameters{
					Prompt: "Choose the better output.",
					Mode:   models.PromptGraderModePairwise,
				},
			},
			{
				Kind:       models.GraderKindText,
				Identifier: "final-output",
				Parameters: models.TextGraderParameters{Contains: []string{"final"}},
			},
		},
		Config: models.Config{
			EngineType: "mock", ModelID: "mock", JudgeModel: "mock",
			TrialsPerTask: 2, MaxAttempts: 2, TimeoutSec: 30,
			SkillPaths: []string{"skills"},
		},
	}
}

func writePairwiseTask(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: task-001
name: Pairwise task
inputs:
  prompt: compare these outputs
`), 0o600))
}

func TestRunBenchmark_PairwiseUsesFinalMatchedBaselineTrials(t *testing.T) {
	dir := t.TempDir()
	writePairwiseTask(t, dir)
	spec := pairwiseTestSpec(true)
	engine := &pairwiseCaptureEngine{MockEngine: execution.NewMockEngine("mock")}
	var partials []*models.EvaluationOutcome
	runner := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		engine,
		WithPartialOutcomeWriter(func(outcome *models.EvaluationOutcome) error {
			partials = append(partials, outcome)
			return nil
		}),
	)

	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.True(t, outcome.IsBaseline)
	require.NotNil(t, outcome.BaselineOutcome)
	require.Equal(t, []bool{true, true, true, false, false}, engine.actorNoSkills)
	require.Equal(t, []string{"baseline-stale", "baseline-1-final", "baseline-2-final"}, engine.baselineOutputs)
	require.Equal(t, []string{"candidate-1-final", "candidate-2-final"}, engine.treatmentOutputs)
	require.Len(t, engine.judgePrompts, 4)
	require.NotContains(t, strings.Join(engine.judgePrompts, "\n"), "baseline-stale")
	for _, prompt := range engine.judgePrompts[:2] {
		require.Contains(t, prompt, "baseline-1-final")
		require.Contains(t, prompt, "candidate-1-final")
	}
	for _, prompt := range engine.judgePrompts[2:] {
		require.Contains(t, prompt, "baseline-2-final")
		require.Contains(t, prompt, "candidate-2-final")
	}

	require.Len(t, outcome.TestOutcomes[0].Runs, 2)
	for _, run := range outcome.TestOutcomes[0].Runs {
		require.Contains(t, run.Validations, "quality")
		require.Contains(t, run.Validations["quality"].Details, "pairwise")
	}
	require.Len(t, outcome.BaselineOutcome.TestOutcomes[0].Runs, 2)
	for _, run := range outcome.BaselineOutcome.TestOutcomes[0].Runs {
		require.NotContains(t, run.Validations, "quality")
	}
	require.Len(t, partials, 4)
	for _, partial := range partials[:2] {
		require.True(t, partial.IsBaseline)
	}
	for _, partial := range partials[2:] {
		require.True(t, partial.IsBaseline)
		require.NotNil(t, partial.BaselineOutcome)
	}
	require.Equal(t, []string{"skills"}, spec.Config.SkillPaths)
}

func TestRunBenchmark_PairwiseRequiresBaselineBeforeEngineInitialization(t *testing.T) {
	dir := t.TempDir()
	writePairwiseTask(t, dir)
	spec := pairwiseTestSpec(false)
	engine := &pairwiseCaptureEngine{MockEngine: execution.NewMockEngine("mock")}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)

	_, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "requires --baseline")
	require.Zero(t, engine.initializeCalls)
	require.Empty(t, engine.actorNoSkills)
}

func TestRunBenchmark_PairwiseRejectsMissingBaselineOutputBeforeTreatment(t *testing.T) {
	dir := t.TempDir()
	writePairwiseTask(t, dir)
	spec := pairwiseTestSpec(true)
	engine := &pairwiseCaptureEngine{MockEngine: execution.NewMockEngine("mock"), failBaseline: true}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)

	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "final baseline attempt")
	require.NotNil(t, outcome)
	require.True(t, outcome.IsBaseline)
	require.Equal(t, true, outcome.Metadata["incomplete"])
	require.Greater(t, outcome.Digest.Errors, 0)
	require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Status)
	require.Len(t, engine.actorNoSkills, 2)
	require.Empty(t, engine.treatmentOutputs)
	require.Empty(t, engine.judgePrompts)
}

func TestRunBenchmark_PairwiseRejectsNameCollisionBeforeEngineInitialization(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: task-001
name: Pairwise task
inputs:
  prompt: compare these outputs
graders:
  - name: quality
    type: text
    config:
      contains: ["final"]
`), 0o600))
	spec := pairwiseTestSpec(true)
	engine := &pairwiseCaptureEngine{MockEngine: execution.NewMockEngine("mock")}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)

	_, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, `pairwise grader name collision "quality"`)
	require.Zero(t, engine.initializeCalls)
	require.Empty(t, engine.actorNoSkills)
	require.Empty(t, engine.judgePrompts)
}

func TestMergeBaselineOutcomes_RequiresExpectedPairwiseResult(t *testing.T) {
	runner := &EvalRunner{pairwiseTaskIDs: map[string]struct{}{"task-001": {}}}
	withSkills := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{
		TestID: "task-001",
		Runs: []models.RunResult{{
			RunNumber: 1,
			Validations: map[string]models.GraderResults{
				"quality": {Name: "quality", Details: map[string]any{"mode": "pairwise"}},
			},
		}},
	}}}
	withoutSkills := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{
		TestID: "task-001",
	}}}

	_, err := runner.mergeBaselineOutcomes(withSkills, withoutSkills)
	require.ErrorContains(t, err, `expected pairwise grader result missing for task "task-001"`)
}

func TestMergeBaselineOutcomes_PreservesOperationalErrorWithoutPairwiseResult(t *testing.T) {
	runner := &EvalRunner{pairwiseTaskIDs: map[string]struct{}{"task-001": {}}}
	candidateRun := models.RunResult{
		RunNumber:   1,
		Status:      models.StatusError,
		ErrorMsg:    "judge session completed without a verdict",
		FinalOutput: "candidate output",
	}
	withSkills := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{
		TestID: "task-001",
		Status: models.StatusError,
		Runs:   []models.RunResult{candidateRun},
	}}}
	withoutSkills := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{
		TestID: "task-001",
		Runs:   []models.RunResult{{RunNumber: 1, Status: models.StatusPassed, FinalOutput: "baseline output"}},
	}}}

	merged, err := runner.mergeBaselineOutcomes(withSkills, withoutSkills)
	require.NoError(t, err)
	require.Same(t, withSkills, merged)
	require.True(t, merged.IsBaseline)
	require.Same(t, withoutSkills, merged.BaselineOutcome)
	require.Equal(t, models.StatusError, merged.TestOutcomes[0].Status)
	require.Equal(t, candidateRun, merged.TestOutcomes[0].Runs[0])
	require.NotNil(t, merged.TestOutcomes[0].SkillImpact)
	require.Nil(t, merged.TestOutcomes[0].SkillImpact.PairwiseSummary)
}

func TestSummarizePairwiseJudgmentsExcludesPositionInconsistentComparisons(t *testing.T) {
	outcome := &models.TestOutcome{Runs: []models.RunResult{{
		Validations: map[string]models.GraderResults{
			"consistent-tie": {Details: map[string]any{
				"pairwise": &models.PairwiseResult{Winner: "tie", PositionConsistent: true},
			}},
			"inconsistent": {Details: map[string]any{
				"pairwise": &models.PairwiseResult{Winner: "inconsistent", PositionConsistent: false},
			}},
		},
	}}}

	summary := summarizePairwiseJudgments(outcome)
	require.Equal(t, &models.PairwiseSummary{Ties: 1, Inconsistent: 1, Judgments: 1}, summary)
}

func TestRunBenchmark_PairwiseRejectsDuplicateSelectedTaskIDs(t *testing.T) {
	for _, tt := range []struct {
		name        string
		ids         []string
		pairwise    bool
		filter      []string
		duplicateID string
	}{
		{name: "pairwise and independent task", ids: []string{"same", "same"}, pairwise: true, duplicateID: "same"},
		{name: "independent tasks also collide", ids: []string{"pairwise", "same", "same"}, pairwise: true, duplicateID: "same"},
		{name: "unique IDs", ids: []string{"pairwise", "other"}, pairwise: true},
		{name: "unselected duplicate", ids: []string{"same", "same"}, pairwise: true, filter: []string{"task-0"}},
		{name: "legacy nonpairwise duplicates", ids: []string{"same", "same"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := pairwiseTestSpec(true)
			spec.Tasks = []string{"task-*.yaml"}
			spec.Graders = nil
			spec.Config.TrialsPerTask = 1
			spec.Config.MaxAttempts = 1
			for i, id := range tt.ids {
				task := fmt.Sprintf("id: %s\nname: task-%d\ninputs:\n  prompt: compare\n", id, i)
				if i == 0 && tt.pairwise {
					task += "graders:\n  - name: quality\n    type: prompt\n    config:\n      mode: pairwise\n      prompt: Choose the better output.\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("task-%d.yaml", i)), []byte(task), 0o600))
			}
			engine := &pairwiseCaptureEngine{MockEngine: execution.NewMockEngine("mock")}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithTaskFilters(tt.filter...))
			selected, err := runner.admissionTestCases()
			require.NoError(t, err)
			require.NotEmpty(t, selected)
			require.Equal(t, tt.pairwise, graders.HasPairwisePromptGraders(nil, selected[0]))

			outcome, err := runner.RunBenchmark(t.Context())
			if tt.duplicateID != "" {
				require.ErrorContains(t, err, fmt.Sprintf("duplicate selected task ID %q", tt.duplicateID))
				require.Nil(t, outcome)
				require.Zero(t, engine.initializeCalls)
				require.Empty(t, engine.actorNoSkills)
				return
			}
			require.NoError(t, err)
			expectedTasks := len(tt.ids)
			if len(tt.filter) > 0 {
				expectedTasks = 1
			}
			require.Len(t, outcome.TestOutcomes, expectedTasks)
			require.NotEmpty(t, engine.actorNoSkills)
		})
	}
}

func TestRunBenchmark_PairwiseRejectsDiscardedOutputAfterRetryExecutionError(t *testing.T) {
	dir := t.TempDir()
	writePairwiseTask(t, dir)
	spec := pairwiseTestSpec(true)
	spec.Config.TrialsPerTask = 1
	engine := &pairwiseCaptureEngine{
		MockEngine:         execution.NewMockEngine("mock"),
		errorBaselineRetry: true,
	}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)

	_, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "final baseline attempt 2 produced no successful output")
	require.Equal(t, []string{"baseline-stale", ""}, engine.baselineOutputs)
	require.Equal(t, []bool{true, true}, engine.actorNoSkills)
	baseline, found := runner.pairwiseBaselineOutput("task-001", 1)
	require.True(t, found)
	require.Equal(t, 2, baseline.attempt)
	require.False(t, baseline.present)
	require.Empty(t, baseline.output)
	require.Empty(t, engine.treatmentOutputs)
	require.Empty(t, engine.judgePrompts)
}
