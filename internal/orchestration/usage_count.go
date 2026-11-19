package orchestration

import (
	"context"
	"sync"

	"github.com/microsoft/waza/internal/execution"
)

type taskExecutionCounterKey struct{}
type taskExecutionMarkerKey struct{}

type taskExecutionCounter struct {
	mu           sync.Mutex
	lower, upper int
}

type taskExecutionMarker struct {
	counter           *taskExecutionCounter
	observed, unknown bool
}

func (m *taskExecutionMarker) finish() {
	m.counter.mu.Lock()
	defer m.counter.mu.Unlock()
	if m.observed {
		m.counter.lower++
		m.counter.upper++
	} else if m.unknown {
		m.counter.upper++
	}
}

func (c *taskExecutionCounter) bounds() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lower, c.upper
}

// executeActorRecorded observes only actor turns, not responder or grader calls.
func (r *EvalRunner) executeActorRecorded(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	marker, ok := ctx.Value(taskExecutionMarkerKey{}).(*taskExecutionMarker)
	if !ok {
		return execution.ExecuteRecorded(ctx, r.engine, req)
	}

	// Exact concrete types avoid trusting an embedded engine's promoted methods
	// when a wrapper overrides Execute without forwarding the observation.
	trusted := false
	switch r.engine.(type) {
	case *execution.MockEngine, *execution.CopilotEngine:
		trusted = true
	}
	var mu sync.Mutex
	active, dispatched, observed := true, false, false
	if trusted {
		ctx = execution.WithActorDispatchObserver(ctx, func() {
			mu.Lock()
			defer mu.Unlock()
			if active {
				dispatched = true
			}
		})
		ctx = execution.WithActorStartObserver(ctx, func() {
			mu.Lock()
			defer mu.Unlock()
			if active {
				observed = true
			}
		})
	}
	resp, err := execution.ExecuteRecorded(ctx, r.engine, req)
	mu.Lock()
	active = false
	marker.observed = marker.observed || observed
	marker.unknown = marker.unknown || !trusted || (dispatched && !observed)
	mu.Unlock()
	return resp, err
}
