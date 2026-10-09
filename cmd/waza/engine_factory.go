package main

import (
	"fmt"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

var newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
	switch cfg.EngineType {
	case "mock":
		return execution.NewMockEngine(cfg.ModelID), nil
	case "copilot-sdk":
		return execution.NewCopilotEngineBuilder(cfg.ModelID, &execution.CopilotEngineBuilderOptions{
			NewCopilotClient: newCopilotClientFn,
		}).Build(), nil
	default:
		return nil, fmt.Errorf("unknown engine type: %s", cfg.EngineType)
	}
}
