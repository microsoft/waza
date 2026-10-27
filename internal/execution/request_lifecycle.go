package execution

import "context"

func (e *CopilotEngine) startContext(ctx context.Context) context.Context {
	if e.requestOwnedLifecycle {
		return ctx
	}
	return context.Background()
}
