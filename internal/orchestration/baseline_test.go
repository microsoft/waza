package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
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
