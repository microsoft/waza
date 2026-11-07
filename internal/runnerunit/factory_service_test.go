package runnerunit

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestServiceNativeEngineFactoryPreservesRetainedMockAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "mock", true: "canceled"}[canceled], func(t *testing.T) {
			calls := 0
			cfg := models.Config{EngineType: "mock", ModelID: "retained-factory"}
			retained := execution.NewMockEngine(cfg.ModelID)
			service := NewService(func(actual models.Config) (execution.AgentEngine, error) {
				calls++
				require.Equal(t, cfg, actual)
				if canceled {
					return nil, context.Canceled
				}
				return retained, nil
			})
			factory := service.NativeEngineFactory(func() (*execution.OwnedRuntimeObservations, error) {
				t.Fatal("mock must not select or bind native observations")
				return nil, nil
			})
			require.Zero(t, calls)
			engine, err := factory(cfg)
			require.Equal(t, 1, calls)
			if canceled {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, engine)
			} else {
				require.NoError(t, err)
				require.Same(t, retained, engine)
			}
		})
	}
}

func TestServiceNativeEngineFactoryUsesSharedNativeBuilderWithoutFallback(t *testing.T) {
	service := NewService(func(models.Config) (execution.AgentEngine, error) {
		t.Fatal("native selection must not invoke the retained nonnative factory")
		return nil, nil
	})
	sink := execution.NewOwnedRuntimeObservations()
	calls := 0
	factory := service.NativeEngineFactory(func() (*execution.OwnedRuntimeObservations, error) {
		calls++
		return sink, nil
	})
	require.Zero(t, calls)
	engine, err := factory(models.Config{EngineType: "copilot-sdk", ModelID: "builder-binding-only"})
	require.NoError(t, err)
	require.IsType(t, &execution.CopilotEngine{}, engine)
	require.Equal(t, 1, calls)
	snapshot, err := sink.Seal()
	require.NoError(t, err)
	require.True(t, snapshot.OwnedEngineBound)
	require.Empty(t, snapshot.Observations)
	require.Equal(t, "unknown", snapshot.ProcessExit)
}

func TestServiceNativeEngineFactoryActualSubmitCancellationAndReuse(t *testing.T) {
	backend := submissionBackend(t)
	calls := 0
	service := NewService(func(models.Config) (execution.AgentEngine, error) {
		calls++
		return nil, context.Canceled
	})
	hooks := SubmissionHooks{Factory: service.NativeEngineFactory(func() (*execution.OwnedRuntimeObservations, error) {
		t.Fatal("mock submission must not select native observations")
		return nil, nil
	})}
	original, err := service.Submit(t.Context(), submissionWire(t, fixture(t), nil), backend, hooks)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "completed", original.Status)
	require.Equal(t, 3, original.ExitCode)
	require.NotNil(t, original.Completion)
	source := executionattempt.Source{
		ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
	}
	reused, err := service.Submit(t.Context(), submissionWire(t, fixture(t), &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, hooks)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, original.Terminal, reused.Terminal)
}
