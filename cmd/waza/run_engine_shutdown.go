package main

import (
	"context"
	"errors"
	"time"

	"github.com/microsoft/waza/internal/execution"
)

var runEngineShutdownGrace = 2 * time.Second

func shutdownRunEngine(parent context.Context, engine execution.AgentEngine) error {
	shutdownCtx, cancelShutdown := context.WithCancel(context.WithoutCancel(parent))
	defer cancelShutdown()

	done := make(chan error, 1)
	go func() {
		done <- engine.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-done:
		return err
	case <-parent.Done():
	}
	cancelShutdown()

	timer := time.NewTimer(runEngineShutdownGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(parent.Err(), err)
	case <-timer.C:
	}

	if force, ok := engine.(interface{ ForceStop() }); ok {
		go force.ForceStop()
	}

	timer.Reset(runEngineShutdownGrace)
	select {
	case err := <-done:
		return errors.Join(parent.Err(), err)
	case <-timer.C:
		return errors.New("engine shutdown exceeded cancellation grace after force stop")
	}
}
