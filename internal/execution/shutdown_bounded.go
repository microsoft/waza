package execution

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Default bounds for ShutdownBounded.
const (
	DefaultShutdownGrace   = 2 * time.Second
	DefaultShutdownTimeout = 30 * time.Second
)

// ShutdownBounded shuts engine down without letting a stalled shutdown block
// forever. When parent is done or maxDuration elapses, the shutdown context is
// canceled; after grace the engine is force-stopped if it supports
// ForceStop(), and after another grace the call returns an error.
func ShutdownBounded(parent context.Context, engine AgentEngine, grace, maxDuration time.Duration) error {
	shutdownCtx, cancelShutdown := context.WithCancel(context.WithoutCancel(parent))
	defer cancelShutdown()

	done := make(chan error, 1)
	go func() {
		done <- engine.Shutdown(shutdownCtx)
	}()

	timeout := time.NewTimer(maxDuration)
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

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(triggerErr, err)
	case <-timer.C:
	}

	if force, ok := engine.(interface{ ForceStop() }); ok {
		go force.ForceStop()
	}

	timer.Reset(grace)
	select {
	case err := <-done:
		return errors.Join(triggerErr, err)
	case <-timer.C:
		return fmt.Errorf("%w; shutdown exceeded grace after force stop", triggerErr)
	}
}
