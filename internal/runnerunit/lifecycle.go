package runnerunit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// runtimeShutdownBound limits runtime Shutdown after execution; exceeding it
// is recorded as a forced stop.
var runtimeShutdownBound = 5 * time.Second

// lifecycleRecorder appends journal events in order off the execution path,
// so storage latency never consumes the attempt's deadline. A pending event
// already proves liveness, so heartbeats coalesce behind it.
type lifecycleRecorder struct {
	journal *executionlifecycle.Journal
	ctx     context.Context

	mu      sync.Mutex
	queue   []executionlifecycle.Event
	running bool
	stopped bool
	idle    chan struct{} // closed when no drain is running
}

func newLifecycleRecorder(ctx context.Context, journal *executionlifecycle.Journal) *lifecycleRecorder {
	idle := make(chan struct{})
	close(idle)
	return &lifecycleRecorder{journal: journal, ctx: context.WithoutCancel(ctx), idle: idle}
}

func (r *lifecycleRecorder) record(event executionlifecycle.Event) {
	if r.journal == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped || (event.Event == executionlifecycle.Heartbeat && len(r.queue) > 0) {
		return
	}
	r.queue = append(r.queue, event)
	if !r.running {
		r.running = true
		r.idle = make(chan struct{})
		go r.drain()
	}
}

func (r *lifecycleRecorder) drain() {
	for {
		r.mu.Lock()
		if len(r.queue) == 0 || r.stopped {
			r.queue, r.running = nil, false
			close(r.idle)
			r.mu.Unlock()
			return
		}
		event := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		// Failures are retained by journal.Err and end later appends.
		_ = r.journal.Append(r.ctx, event)
	}
}

// close waits up to timeout for queued events. On timeout it drops the rest
// and waits for the in-flight append, which is itself bounded.
func (r *lifecycleRecorder) close(timeout time.Duration) error {
	r.mu.Lock()
	idle := r.idle
	r.mu.Unlock()
	wait := time.NewTimer(timeout)
	defer wait.Stop()
	select {
	case <-idle:
		return nil
	case <-wait.C:
	}
	r.mu.Lock()
	r.stopped = true
	idle = r.idle
	r.mu.Unlock()
	grace := time.NewTimer(executionlifecycle.DefaultWriteBound)
	defer grace.Stop()
	select {
	case <-idle:
	case <-grace.C:
	}
	return fmt.Errorf("%w: lifecycle events were dropped after %s", executionlifecycle.ErrWriteStalled, timeout)
}

// trackLifecycle records owner liveness and cancellation around one dispatched
// execution. Journal failures never change the execution outcome; the first
// failure ends recording and is reported by journal.Err.
func trackLifecycle(ctx context.Context, recorder *lifecycleRecorder, interval time.Duration,
	run func(context.Context) Report,
) Report {
	now := func() time.Time { return time.Now().UTC() }
	started := executionlifecycle.Event{Event: executionlifecycle.Started, At: now(),
		HeartbeatIntervalMS: max(interval.Milliseconds(), 1)}
	if deadline, ok := ctx.Deadline(); ok {
		deadline = deadline.UTC()
		started.DeadlineAt = &deadline
	}
	recorder.record(started)

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
				recorder.record(executionlifecycle.Event{Event: executionlifecycle.Heartbeat, At: now()})
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
		recorder.record(executionlifecycle.Event{Event: executionlifecycle.CancelRequested, At: now(), Reason: reason})
	})

	report := run(ctx)

	if !stopRequest() {
		<-requestDone
	}
	close(done)
	heartbeats.Wait()
	switch {
	case report.forced:
		recorder.record(executionlifecycle.Event{Event: executionlifecycle.Forced, At: now()})
	case requested:
		recorder.record(executionlifecycle.Event{Event: executionlifecycle.CancelAcknowledged, At: now()})
	}
	return report
}

func finishLifecycle(recorder *lifecycleRecorder, outcome runnerterminal.Outcome) {
	recorder.record(executionlifecycle.Event{Event: executionlifecycle.Finished,
		At: time.Now().UTC(), Outcome: outcome})
}
