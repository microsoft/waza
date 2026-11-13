package execution

import "context"

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

// ActivityObserverFrom returns the observer attached to ctx, or nil.
func ActivityObserverFrom(ctx context.Context) ActivityObserver {
	observer, _ := ctx.Value(activityObserverKey{}).(ActivityObserver)
	return observer
}
