package execution

import (
	"context"
	"errors"
)

// ActivityObserver receives live model-turn and tool-execution starts so an
// embedding host can enforce counted budgets while the attempt is running.
// Calls may arrive concurrently and must not block.
type ActivityObserver interface {
	ModelTurnStarted()
	ToolCallStarted()
}

type activityObserverKey struct{}

// WithActivityObserver attaches observer to executions using ctx.
func WithActivityObserver(ctx context.Context, observer ActivityObserver) context.Context {
	return context.WithValue(ctx, activityObserverKey{}, observer)
}

// activityPreempter is implemented by observers whose enforcement yields to
// an execution deadline that ended the attempt first.
type activityPreempter interface{ Preempt() }

// ActivityObserverFrom returns the observer attached to ctx, or nil. Activity
// reported after ctx's own deadline first preempts a preemptible observer, so
// work drained after an execution timeout cannot claim that timeout.
func ActivityObserverFrom(ctx context.Context) ActivityObserver {
	observer, _ := ctx.Value(activityObserverKey{}).(ActivityObserver)
	if _, ok := observer.(activityPreempter); ok {
		return deadlineObserver{ctx: ctx, ActivityObserver: observer}
	}
	return observer
}

type deadlineObserver struct {
	ctx context.Context
	ActivityObserver
}

func (o deadlineObserver) ModelTurnStarted() {
	yieldToDeadline(o.ctx, o.ActivityObserver)
	o.ActivityObserver.ModelTurnStarted()
}

func (o deadlineObserver) ToolCallStarted() {
	yieldToDeadline(o.ctx, o.ActivityObserver)
	o.ActivityObserver.ToolCallStarted()
}

func (o deadlineObserver) Preempt() { o.ActivityObserver.(activityPreempter).Preempt() }

// yieldToDeadline preempts observer once ctx has reached its deadline. An
// observer that itself ended the attempt ignores the preemption.
func yieldToDeadline(ctx context.Context, observer ActivityObserver) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		observer.(activityPreempter).Preempt()
	}
}

// preemptOnDeadline preempts a preemptible observer as soon as ctx's deadline
// ends an execution, before the engine drains. The returned function stops it.
func preemptOnDeadline(ctx context.Context) func() bool {
	observer, _ := ctx.Value(activityObserverKey{}).(ActivityObserver)
	if _, ok := observer.(activityPreempter); !ok {
		return func() bool { return false }
	}
	return context.AfterFunc(ctx, func() { yieldToDeadline(ctx, observer) })
}
