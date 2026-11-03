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

type timeoutThenContinueEngine struct {
	calls int
}

func (*timeoutThenContinueEngine) Initialize(context.Context) error { return nil }
func (*timeoutThenContinueEngine) Shutdown(context.Context) error   { return nil }
func (*timeoutThenContinueEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *timeoutThenContinueEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	if e.calls == 2 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &execution.ExecutionResponse{Success: true, FinalOutput: "completed"}, nil
}

type cancelDuringSecondEngine struct {
	calls  int
	cancel context.CancelFunc
}

type countingSuccessEngine struct {
	calls int
}

func (*countingSuccessEngine) Initialize(context.Context) error { return nil }
func (*countingSuccessEngine) Shutdown(context.Context) error   { return nil }
func (*countingSuccessEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *countingSuccessEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	return &execution.ExecutionResponse{Success: true, FinalOutput: "completed"}, nil
}

func (*cancelDuringSecondEngine) Initialize(context.Context) error { return nil }
func (*cancelDuringSecondEngine) Shutdown(context.Context) error   { return nil }
func (*cancelDuringSecondEngine) SessionUsage(string) *models.UsageStats {
	return nil
}
func (e *cancelDuringSecondEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	if e.calls == 2 {
		e.cancel()
		return &execution.ExecutionResponse{FinalOutput: "partial output"}, context.Canceled
	}
	return &execution.ExecutionResponse{Success: true, FinalOutput: "completed"}, nil
}

func TestRunBenchmarkPublishesPartialOutcomeAfterEachTrial(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: partial
name: Partial results
inputs:
  prompt: keep completed trials
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "partial-results"},
		SkillName:    "partial-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "test-model",
			TrialsPerTask: 2,
			TimeoutSec:    30,
		},
	}

	var partials []*models.EvaluationOutcome
	runner := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		execution.NewMockEngine("test-model"),
		WithPartialOutcomeWriter(func(outcome *models.EvaluationOutcome) error {
			partials = append(partials, outcome)
			return nil
		}),
	)

	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 1)
	require.Len(t, outcome.TestOutcomes[0].Runs, 2)
	require.Len(t, partials, 2)
	require.Equal(t, true, partials[0].Metadata["incomplete"])
	require.Len(t, partials[0].TestOutcomes[0].Runs, 1)
	require.Len(t, partials[1].TestOutcomes[0].Runs, 2)
}

func TestRunBenchmarkSurfacesPartialOutcomeWriteFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: partial-error
name: Partial write error
inputs:
  prompt: fail the writer
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "partial-error"},
		SkillName:    "partial-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "test-model",
			TrialsPerTask: 2,
			TimeoutSec:    30,
		},
	}

	runner := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		execution.NewMockEngine("test-model"),
		WithPartialOutcomeWriter(func(*models.EvaluationOutcome) error {
			return os.ErrPermission
		}),
	)

	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "write partial results")
	require.ErrorIs(t, err, os.ErrPermission)
	require.NotNil(t, outcome)
	require.Len(t, outcome.TestOutcomes, 1)
	require.Len(t, outcome.TestOutcomes[0].Runs, 1)
}

func TestRunBenchmarkRecordsTimeoutAndContinuesNextTrial(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: timeout
name: Timeout recovery
inputs:
  prompt: continue after timeout
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "timeout-recovery"},
		SkillName:    "timeout-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "copilot-sdk",
			ModelID:       "test-model",
			TrialsPerTask: 3,
			TimeoutSec:    1,
		},
	}
	engine := &timeoutThenContinueEngine{}
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
	require.Equal(t, 3, engine.calls)
	require.Len(t, outcome.TestOutcomes[0].Runs, 3)
	require.Equal(t, models.StatusPassed, outcome.TestOutcomes[0].Runs[0].Status)
	require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Runs[1].Status)
	require.Contains(t, outcome.TestOutcomes[0].Runs[1].ErrorMsg, context.DeadlineExceeded.Error())
	require.Equal(t, models.StatusPassed, outcome.TestOutcomes[0].Runs[2].Status)
	require.Len(t, partials, 3)
}

func TestRunBenchmarkCancellationPublishesCompletedTrials(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: cancel
name: Cancellation recovery
inputs:
  prompt: preserve completed trials
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "cancellation-recovery"},
		SkillName:    "cancel-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "copilot-sdk",
			ModelID:       "test-model",
			TrialsPerTask: 3,
			TimeoutSec:    30,
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	engine := &cancelDuringSecondEngine{cancel: cancel}
	var partials []*models.EvaluationOutcome
	runner := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		engine,
		WithPartialOutcomeWriter(func(outcome *models.EvaluationOutcome) error {
			partials = append(partials, outcome)
			return nil
		}),
	)

	outcome, err := runner.RunBenchmark(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 2, engine.calls)
	require.Len(t, outcome.TestOutcomes[0].Runs, 2)
	require.Equal(t, models.StatusPassed, outcome.TestOutcomes[0].Runs[0].Status)
	require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Runs[1].Status)
	require.Equal(t, "partial output", outcome.TestOutcomes[0].Runs[1].FinalOutput)
	require.Len(t, partials, 2)
	require.Len(t, partials[1].TestOutcomes[0].Runs, 2)
}

func TestRunBenchmarkDoesNotCacheIncompleteTask(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: cache-interruption
name: Cache interruption
inputs:
  prompt: do not cache incomplete work
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "cache-interruption"},
		SkillName:    "cache-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "copilot-sdk",
			ModelID:       "test-model",
			TrialsPerTask: 3,
			TimeoutSec:    30,
		},
	}
	resultCache := cache.New(filepath.Join(dir, "cache"))
	ctx, cancel := context.WithCancel(t.Context())
	firstEngine := &cancelDuringSecondEngine{cancel: cancel}
	first := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		firstEngine,
		WithCache(resultCache),
	)

	outcome, err := first.RunBenchmark(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, outcome.TestOutcomes[0].Runs, 2)

	secondEngine := &countingSuccessEngine{}
	second := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		secondEngine,
		WithCache(resultCache),
	)
	outcome, err = second.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Equal(t, 3, secondEngine.calls)
	require.False(t, outcome.TestOutcomes[0].Cached)
	require.Len(t, outcome.TestOutcomes[0].Runs, 3)
}

func TestRunBenchmarkPublishesCacheHitsInPartialOutcome(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: cached-partial
name: Cached partial
inputs:
  prompt: include cache hits
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "cached-partial"},
		SkillName:    "cache-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "test-model",
			TrialsPerTask: 1,
			TimeoutSec:    30,
		},
	}
	resultCache := cache.New(filepath.Join(dir, "cache"))
	firstEngine := &countingSuccessEngine{}
	first := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		firstEngine,
		WithCache(resultCache),
	)
	_, err := first.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, firstEngine.calls)

	secondEngine := &countingSuccessEngine{}
	var partials []*models.EvaluationOutcome
	second := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		secondEngine,
		WithCache(resultCache),
		WithPartialOutcomeWriter(func(outcome *models.EvaluationOutcome) error {
			partials = append(partials, outcome)
			return nil
		}),
	)
	outcome, err := second.RunBenchmark(t.Context())
	require.NoError(t, err)
	require.Zero(t, secondEngine.calls)
	require.True(t, outcome.TestOutcomes[0].Cached)
	require.Len(t, partials, 1)
	require.True(t, partials[0].TestOutcomes[0].Cached)
	require.Len(t, partials[0].TestOutcomes[0].Runs, 1)
}

func TestBaselinePartialOutcomePreservesBothPasses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`
id: baseline-partial
name: Baseline partial
inputs:
  prompt: preserve both passes
`), 0o600))

	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "baseline-partial"},
		SkillName:    "baseline-skill",
		Tasks:        []string{"task.yaml"},
		Config: models.Config{
			EngineType:    "mock",
			ModelID:       "test-model",
			TrialsPerTask: 1,
			TimeoutSec:    30,
		},
	}
	var partials []*models.EvaluationOutcome
	runner := NewEvalRunner(
		config.NewEvalConfig(spec, config.WithSpecDir(dir)),
		&countingSuccessEngine{},
		WithPartialOutcomeWriter(func(outcome *models.EvaluationOutcome) error {
			partials = append(partials, outcome)
			return nil
		}),
	)

	withSkills, err := runner.runNormalBenchmark(t.Context())
	require.NoError(t, err)
	runner.partialMu.Lock()
	runner.partialPrimary = withSkills
	runner.partialMu.Unlock()
	_, err = runner.runNormalBenchmark(t.Context())
	require.NoError(t, err)

	require.Len(t, partials, 2)
	require.True(t, partials[1].IsBaseline)
	require.Len(t, partials[1].TestOutcomes, 1)
	require.NotNil(t, partials[1].BaselineOutcome)
	require.Len(t, partials[1].BaselineOutcome.TestOutcomes, 1)
}
