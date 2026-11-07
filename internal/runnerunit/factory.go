package runnerunit

import (
	"fmt"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// NewEngineFactory selects a fresh concrete engine without changing admission.
// An optional runtime getter runs only for native execution, after reservation
// when used by Submit; nil preserves the legacy default construction.
func NewEngineFactory(runtime func() (*execution.OwnedRuntimeObservations, error)) EngineFactory {
	return func(cfg models.Config) (execution.AgentEngine, error) {
		if cfg.EngineType != "copilot-sdk" {
			return execution.NewMockEngine(cfg.ModelID), nil
		}
		var observations *execution.OwnedRuntimeObservations
		if runtime != nil {
			var err error
			observations, err = runtime()
			if err != nil {
				return nil, fmt.Errorf("selecting owned runtime observations: %w", err)
			}
			if observations == nil {
				return nil, fmt.Errorf("%w: runtime selector returned no owned observations", execution.ErrRuntimeObservation)
			}
		}
		return execution.NewCopilotEngineBuilder(cfg.ModelID, &execution.CopilotEngineBuilderOptions{
			OwnClient: true, DisableCommandMocks: true,
			DisableAmbientDiscovery: true, RuntimeObservations: observations,
			// Preserve diagnostic terminalization without a second logging sink.
			DiagnosticObserver: func(execution.ExecutionDiagnostic) error { return nil },
		}).Build(), nil
	}
}

// NativeEngineFactory selects owned native observations while retaining the
// Service's existing nonnative factory, including its lifecycle/error behavior.
func (s *Service) NativeEngineFactory(runtime func() (*execution.OwnedRuntimeObservations, error)) EngineFactory {
	native := NewEngineFactory(runtime)
	return func(cfg models.Config) (execution.AgentEngine, error) {
		if cfg.EngineType == "copilot-sdk" {
			return native(cfg)
		}
		return s.factory(cfg)
	}
}
