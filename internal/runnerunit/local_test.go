package runnerunit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func localRequest(t *testing.T) []byte {
	t.Helper()
	eval, err := os.ReadFile("../runnercompat/testdata/eval-1.4.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("../runnercompat/testdata/task.yaml")
	require.NoError(t, err)
	// Keep the service fixture independent of its upstream compatibility caller.
	return wire(t, runnercontract.Request{
		SchemaVersion: "1.0", Versions: []string{"1.0"},
		RequiredCapabilities: []string{"exact-unit", "inline.mock-text"}, OptionalCapabilities: []string{},
		RunKey: "local", Selection: runnercontract.Selection{Shard: "local", Task: "selected", Trial: 1, Attempt: 1},
		Input: runnercontract.Input{Shard: "local", Eval: string(eval), Task: string(task)}, TimeoutMS: 30000,
	})
}

func TestLocalServiceUsesOneAdmittedMockAndHistoricalAggregation(t *testing.T) {
	constructed := 0
	service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		constructed++
		require.Equal(t, "mock-model", cfg.ModelID)
		return execution.NewMockEngine(cfg.ModelID), nil
	})
	outcome, err := service.RunLocal(t.Context(), localRequest(t))
	require.NoError(t, err)
	require.Equal(t, 1, constructed)
	require.Equal(t, "1.7", outcome.SchemaVersion)
	require.Equal(t, "adapter-frozen", outcome.BenchName)
	require.Len(t, outcome.TestOutcomes, 1)
	task := outcome.TestOutcomes[0]
	require.Equal(t, "selected", task.TestID)
	require.Len(t, task.Runs, 1)
	require.Equal(t, 1, task.Stats.TotalRuns)
	require.Equal(t, 1.0, task.Stats.AvgScore)
	require.Equal(t, 1, outcome.Digest.Succeeded)
	require.Len(t, task.Runs[0].Validations, 5)
	require.Len(t, outcome.EvaluationUsage.Sessions, 1)
	require.Equal(t, task.Runs[0].SessionDigest.SessionID, outcome.EvaluationUsage.Sessions[0].SessionID)
	require.Nil(t, outcome.EvaluationUsage.Sessions[0].Usage)
	require.Nil(t, outcome.Digest.Usage)
	_, err = os.Stat(task.Runs[0].WorkspaceDir)
	require.ErrorIs(t, err, os.ErrNotExist, "service must finish cleanup before publication")
}

func TestLocalServiceErrorsNeverReturnOutcome(t *testing.T) {
	request := localRequest(t)
	sentinel := errors.New("injected local startup")
	for _, control := range []struct {
		name    string
		data    []byte
		factory EngineFactory
		cancel  bool
		cause   error
	}{
		{name: "invalid request", data: []byte(`{}`)},
		{name: "canceled", data: request, cancel: true, cause: context.Canceled},
		{name: "startup failure", data: request, factory: func(models.Config) (execution.AgentEngine, error) { return nil, sentinel }, cause: sentinel},
		{name: "nil mock", data: request, factory: func(models.Config) (execution.AgentEngine, error) { return (*execution.MockEngine)(nil), nil }},
		{name: "wrapper denied", data: request, factory: func(models.Config) (execution.AgentEngine, error) {
			return &instrumented{AgentEngine: execution.NewMockEngine("mock")}, nil
		}},
	} {
		t.Run(control.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if control.cancel {
				cancel()
			}

			outcome, err := NewService(control.factory).RunLocal(ctx, control.data)
			require.Error(t, err)
			require.Nil(t, outcome)
			if control.cause != nil {
				require.ErrorIs(t, err, control.cause)
			}
		})
	}
}

func TestLocalServicePreservesObservedFailureWithoutChangingTerminalPolicy(t *testing.T) {
	request := localRequest(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, missing)
	}
	service := NewService(nil)
	report := service.run(t.Context(), request, true)
	require.ErrorIs(t, report.failure, os.ErrNotExist, "retain the actual typed execution cause")
	require.Equal(t, 1, report.Calls)
	require.NotNil(t, report.localOutcome)
	require.Equal(t, models.StatusError, report.Observation.Status)
	outcome, err := service.RunLocal(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, 1, outcome.Digest.Failed)
	require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Runs[0].Status)
	envelope, _ := service.RunTerminal(t.Context(), request)
	require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
	require.Equal(t, 2, envelope.ExitCode)
	require.Equal(t, 1, envelope.EngineCalls)
	require.Equal(t, string(models.StatusError), envelope.Observation.Status)
}
