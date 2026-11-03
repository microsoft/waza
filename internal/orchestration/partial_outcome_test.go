package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
