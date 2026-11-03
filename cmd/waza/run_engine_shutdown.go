package main

import (
	"context"
	"errors"
	"time"

	"github.com/microsoft/waza/internal/execution"
)

var runEngineShutdownGrace = 2 * time.Second

func shutdownRunEngine(parent context.Context, engine execution.AgentEngine) error {
	if parent.Err() == nil {
		return engine.Shutdown(context.Background())
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runEngineShutdownGrace)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- engine.Shutdown(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	if force, ok := engine.(interface{ ForceStop() }); ok {
		force.ForceStop()
	}

	timer := time.NewTimer(runEngineShutdownGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(ctx.Err(), err)
	case <-timer.C:
		return errors.New("engine shutdown exceeded cancellation grace after force stop")
	}
}
