package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRequestOwnedLifecycleCancelsStalledStartupAndCleansClient(t *testing.T) {
	client := NewMockCopilotClient(gomock.NewController(t))
	engine := NewCopilotEngineBuilder("fixed", &CopilotEngineBuilderOptions{
		NewCopilotClient:      func(*copilot.ClientOptions) CopilotClient { return client },
		RequestOwnedLifecycle: true,
		AllowUnavailableUsage: true,
		DiagnosticObserver:    func(ExecutionDiagnostic) error { return nil },
	}).Build()
	engine.provider = customProviderConfig{}
	started := make(chan struct{})
	client.EXPECT().Start(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	client.EXPECT().Stop().Return(nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- engine.Initialize(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "start_failed")
	case <-time.After(5 * time.Second):
		t.Fatal("request cancellation did not interrupt native startup")
	}
	require.NoError(t, engine.Shutdown(t.Context()))
	require.NoError(t, engine.Shutdown(t.Context()), "cleanup is idempotent")
}

func TestRequestOwnedUnavailableUsageDoesNotHideCleanupFailure(t *testing.T) {
	var seen []ExecutionDiagnostic
	state := &diagnosticState{
		allowUnavailableUsage: true,
		observer:              func(d ExecutionDiagnostic) error { seen = append(seen, d); return nil },
	}
	op := &diagnosticOperation{state: state}
	for _, code := range []DiagnosticCode{CodeUsageMissingOrPartial, CodeModelAttributionMissing, CodeShutdownRPC} {
		op.report(StageShutdownUsage, code, "session")
	}
	require.NoError(t, op.err())
	require.Len(t, seen, 3, "unavailable billing must remain explicitly observable")
	op.report(StageWorkspaceCleanup, CodeCleanup, "session")
	var diagnostic *DiagnosticError
	require.True(t, errors.As(op.err(), &diagnostic))
	require.Equal(t, StageWorkspaceCleanup, diagnostic.Diagnostic.Stage)
	require.Equal(t, CodeCleanup, diagnostic.Diagnostic.Code)
}
