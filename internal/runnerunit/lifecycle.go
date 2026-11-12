package runnerunit

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// runtimeShutdownBound limits runtime Shutdown after execution; exceeding it
// is recorded as a forced stop.
var runtimeShutdownBound = 5 * time.Second

// trackLifecycle records owner liveness and cancellation around one dispatched
// execution. Journal failures never change the execution outcome; the first
// failure ends recording and is reported by journal.Err.
func trackLifecycle(ctx context.Context, journal *executionlifecycle.Journal, interval time.Duration,
	run func(context.Context) Report,
) Report {
	now := func() time.Time { return time.Now().UTC() }
	started := executionlifecycle.Event{Event: executionlifecycle.Started, At: now(),
		HeartbeatIntervalMS: max(interval.Milliseconds(), 1)}
	if deadline, ok := ctx.Deadline(); ok {
		deadline = deadline.UTC()
		started.DeadlineAt = &deadline
	}
	_ = journal.Append(ctx, started)

	done := make(chan struct{})
	var heartbeats sync.WaitGroup
	heartbeats.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				_ = journal.Append(ctx, executionlifecycle.Event{Event: executionlifecycle.Heartbeat, At: now()})
			}
		}
	})
	requested := false
	requestDone := make(chan struct{})
	stopRequest := context.AfterFunc(ctx, func() {
		defer close(requestDone)
		reason := executionlifecycle.ReasonCaller
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = executionlifecycle.ReasonDeadline
		}
		requested = true
		_ = journal.Append(ctx, executionlifecycle.Event{Event: executionlifecycle.CancelRequested, At: now(), Reason: reason})
	})

	report := run(ctx)

	if !stopRequest() {
		<-requestDone
	}
	close(done)
	heartbeats.Wait()
	switch {
	case report.forced:
		_ = journal.Append(ctx, executionlifecycle.Event{Event: executionlifecycle.Forced, At: now()})
	case requested:
		_ = journal.Append(ctx, executionlifecycle.Event{Event: executionlifecycle.CancelAcknowledged, At: now()})
	}
	return report
}

func finishLifecycle(ctx context.Context, journal *executionlifecycle.Journal, outcome runnerterminal.Outcome) {
	_ = journal.Append(ctx, executionlifecycle.Event{Event: executionlifecycle.Finished,
		At: time.Now().UTC(), Outcome: outcome})
}
