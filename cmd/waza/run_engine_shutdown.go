package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/execution"
)

var runEngineShutdownGrace = 2 * time.Second
var runEngineShutdownTimeout = 30 * time.Second

func shutdownRunEngine(parent context.Context, engine execution.AgentEngine) error {
	shutdownCtx, cancelShutdown := context.WithCancel(context.WithoutCancel(parent))
	defer cancelShutdown()

	done := make(chan error, 1)
	go func() {
		done <- engine.Shutdown(shutdownCtx)
	}()

	timeout := time.NewTimer(runEngineShutdownTimeout)
	defer timeout.Stop()
	var triggerErr error
	select {
	case err := <-done:
		return err
	case <-parent.Done():
		triggerErr = parent.Err()
	case <-timeout.C:
		triggerErr = errors.New("engine shutdown exceeded maximum duration")
	}
	cancelShutdown()

	timer := time.NewTimer(runEngineShutdownGrace)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(triggerErr, err)
	case <-timer.C:
	}

	if force, ok := engine.(interface{ ForceStop() }); ok {
		go force.ForceStop()
	}

	timer.Reset(runEngineShutdownGrace)
	select {
	case err := <-done:
		return errors.Join(triggerErr, err)
	case <-timer.C:
		return fmt.Errorf("%w; shutdown exceeded grace after force stop", triggerErr)
	}
}
