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

type actorStartObserverKey struct{}
type actorDispatchObserverKey struct{}

// WithActorStartObserver attaches an optional observed actor-turn callback.
// The callback may run concurrently and must not block.
func WithActorStartObserver(ctx context.Context, observer func()) context.Context {
	return context.WithValue(ctx, actorStartObserverKey{}, observer)
}

// NotifyActorStart reports an observed actor turn, independently of budgets.
func NotifyActorStart(ctx context.Context) {
	if observer := actorStartObserverFrom(ctx); observer != nil {
		observer()
	}
}

func actorStartObserverFrom(ctx context.Context) func() {
	observer, _ := ctx.Value(actorStartObserverKey{}).(func())
	return observer
}

// WithActorDispatchObserver records that an engine may initiate an actor turn.
// It is not evidence that the actor actually started.
func WithActorDispatchObserver(ctx context.Context, observer func()) context.Context {
	return context.WithValue(ctx, actorDispatchObserverKey{}, observer)
}

// NotifyActorDispatch marks the point beyond which absence of a start signal
// cannot prove that the actor did not start.
func NotifyActorDispatch(ctx context.Context) {
	observer, _ := ctx.Value(actorDispatchObserverKey{}).(func())
	if observer != nil {
		observer()
	}
}

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

func (o deadlineObserver) Preempt() {
	if preempter, ok := o.ActivityObserver.(activityPreempter); ok {
		preempter.Preempt()
	}
}

// yieldToDeadline preempts observer once ctx has reached its deadline. An
// observer that itself ended the attempt ignores the preemption.
func yieldToDeadline(ctx context.Context, observer ActivityObserver) {
	if preempter, ok := observer.(activityPreempter); ok && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		preempter.Preempt()
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
