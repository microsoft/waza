package main

import (
	"context"

	"github.com/microsoft/waza/internal/execution"
)

var runEngineShutdownGrace = execution.DefaultShutdownGrace
var runEngineShutdownTimeout = execution.DefaultShutdownTimeout

func shutdownRunEngine(parent context.Context, engine execution.AgentEngine) error {
	return execution.ShutdownBounded(parent, engine, runEngineShutdownGrace, runEngineShutdownTimeout)
}
