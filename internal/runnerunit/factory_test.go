package runnerunit

import (
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOwnedEngineFactorySelectsNativeSinkLazilyAndRejectsMissingCapture(t *testing.T) {
	cause := errors.New("unreserved runtime control")
	for _, control := range []string{"mock", "error", "nil", "native", "legacy-native"} {
		t.Run(control, func(t *testing.T) {
			calls := 0
			sink := execution.NewOwnedRuntimeObservations()
			var getter func() (*execution.OwnedRuntimeObservations, error)
			if control != "legacy-native" {
				getter = func() (*execution.OwnedRuntimeObservations, error) {
					calls++
					switch control {
					case "mock":
						t.Fatal("mock factory must not select a native runtime sink")
					case "error":
						return nil, cause
					case "nil":
						return nil, nil
					}
					return sink, nil
				}
			}
			factory := NewEngineFactory(getter)
			require.Zero(t, calls, "constructor must not select/construct a runtime")
			config := models.Config{EngineType: "copilot-sdk", ModelID: "factory-binding-only"}
			if control == "mock" {
				config.EngineType = "mock"
			}
			engine, err := factory(config)
			switch control {
			case "error":
				require.ErrorIs(t, err, cause)
				require.Nil(t, engine)
			case "nil":
				require.ErrorIs(t, err, execution.ErrRuntimeObservation)
				require.Nil(t, engine)
			case "mock":
				require.NoError(t, err)
				require.IsType(t, &execution.MockEngine{}, engine)
			default:
				require.NoError(t, err)
				require.IsType(t, &execution.CopilotEngine{}, engine)
				// Builder binding is not SDK launch, execution or process evidence.
				snapshot, err := sink.Seal()
				require.NoError(t, err)
				require.Equal(t, control == "native", snapshot.OwnedEngineBound)
				require.Empty(t, snapshot.Observations)
				require.Equal(t, "unknown", snapshot.ProcessExit)
			}
			if control == "legacy-native" || control == "mock" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}
