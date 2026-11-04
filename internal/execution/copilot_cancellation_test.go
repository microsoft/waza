package execution

import (
	"context"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type forceStopTestClient struct {
	*MockCopilotClient
	forceStop func()
}

func (c *forceStopTestClient) ForceStop() {
	c.forceStop()
}

func TestSendAndWaitBoundedForceStopsContextIgnoringSession(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 10 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	release := make(chan struct{})
	var releaseOnce sync.Once
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-release
			return nil, ctx.Err()
		},
	)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, abandoned, retire, err := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		releaseOnce.Do(func() { close(release) })
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, abandoned)
	require.False(t, retire)
}

func TestSendAndWaitBoundedAbandonsSessionAfterForceStopGrace(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-release
			return nil, nil
		},
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	forced := make(chan struct{}, 1)
	_, abandoned, retire, err := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		forced <- struct{}{}
	})

	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-forced:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("force-stop callback was not invoked")
	}
	require.True(t, abandoned)
	require.True(t, retire)
}

func TestSharedEngineCancellationDoesNotForceStopSiblingSession(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	blockedSession := NewMockCopilotSession(ctrl)
	healthySession := NewMockCopilotSession(ctrl)
	blockedStarted := make(chan struct{})
	healthyStarted := make(chan struct{})
	releaseBlocked := make(chan struct{})
	releaseHealthy := make(chan struct{})
	t.Cleanup(func() { close(releaseBlocked) })
	blockedSession.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			close(blockedStarted)
			<-releaseBlocked
			return nil, context.Canceled
		},
	)
	healthySession.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			close(healthyStarted)
			<-releaseHealthy
			return &copilot.SessionEvent{}, nil
		},
	)
	forced := make(chan struct{}, 1)
	engine := &CopilotEngine{
		client: &forceStopTestClient{forceStop: func() { forced <- struct{}{} }},
	}

	healthyDone := make(chan error, 1)
	go func() {
		_, _, _, err := sendAndWaitBounded(context.Background(), healthySession, copilot.MessageOptions{}, engine.forceStopTransport)
		healthyDone <- err
	}()
	<-healthyStarted

	ctx, cancel := context.WithCancel(t.Context())
	type boundedResult struct {
		abandoned bool
		retire    bool
		err       error
	}
	blockedDone := make(chan boundedResult, 1)
	go func() {
		_, abandoned, retire, err := sendAndWaitBounded(ctx, blockedSession, copilot.MessageOptions{}, engine.forceStopTransport)
		blockedDone <- boundedResult{abandoned: abandoned, retire: retire, err: err}
	}()
	<-blockedStarted
	cancel()
	result := <-blockedDone
	require.ErrorIs(t, result.err, context.Canceled)
	require.True(t, result.abandoned)
	require.True(t, result.retire)
	select {
	case <-forced:
		t.Fatal("shared transport force-stop interrupted a healthy sibling")
	default:
	}

	close(releaseHealthy)
	require.NoError(t, <-healthyDone)
}

func TestDefaultSharedEngineRefreshesRetiredClientForLaterTrial(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")
	t.Setenv("COPILOT_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
	oldConstruct, oldEmbeddedPath := sharedConstruct, embeddedCLIPath
	t.Cleanup(func() {
		sharedConstruct = oldConstruct
		embeddedCLIPath = oldEmbeddedPath
	})
	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }

	ctrl := gomock.NewController(t)
	firstClient := newClientMock(ctrl)
	secondClient := newClientMock(ctrl)
	blockedSession := newSessionMock(ctrl)
	healthySibling := newSessionMock(ctrl)
	replacementSession := newSessionMock(ctrl)
	firstCreate := 0
	firstClient.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *copilot.SessionConfig) (CopilotSession, error) {
			firstCreate++
			if firstCreate == 1 {
				return blockedSession, nil
			}
			return healthySibling, nil
		},
	).Times(2)
	secondClient.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(replacementSession, nil)

	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	blockedSession.EXPECT().SessionID().Return("retired-session")
	blockedSession.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	blockedSession.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			close(sendStarted)
			<-releaseSend
			return nil, context.Canceled
		},
	)
	healthyStarted := make(chan struct{})
	releaseHealthy := make(chan struct{})
	healthySibling.EXPECT().SessionID().Return("healthy-sibling")
	healthySibling.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	healthySibling.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			close(healthyStarted)
			<-releaseHealthy
			return &copilot.SessionEvent{}, nil
		},
	)
	healthySibling.EXPECT().Disconnect().Return(nil)
	firstClient.EXPECT().DeleteSession(gomock.Any(), "healthy-sibling").Return(nil)

	replacementSession.EXPECT().SessionID().Return("replacement-session")
	replacementSession.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	replacementSession.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	replacementSession.EXPECT().Disconnect().Return(nil)
	secondClient.EXPECT().DeleteSession(gomock.Any(), "replacement-session").Return(nil)

	constructions := 0
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
		constructions++
		if constructions == 1 {
			return firstClient
		}
		return secondClient
	}

	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	currentClient, ok := engine.CopilotClient().(*startupClient)
	require.True(t, ok)
	require.Same(t, firstClient, currentClient.CopilotClient)

	ctx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan *ExecutionResponse, 1)
	firstErr := make(chan error, 1)
	go func() {
		response, err := engine.Execute(ctx, &ExecutionRequest{
			Message:          "ignore cancellation",
			ModelID:          "test-model",
			EphemeralSession: true,
		})
		firstDone <- response
		firstErr <- err
	}()
	<-sendStarted

	healthyDone := make(chan *ExecutionResponse, 1)
	healthyErr := make(chan error, 1)
	go func() {
		response, err := engine.Execute(t.Context(), &ExecutionRequest{
			Message:          "healthy sibling",
			ModelID:          "test-model",
			EphemeralSession: true,
		})
		healthyDone <- response
		healthyErr <- err
	}()
	<-healthyStarted

	cancel()
	response, err := <-firstDone, <-firstErr
	require.NoError(t, err)
	require.False(t, response.Success)
	require.True(t, engine.retired.Load())

	response, err = engine.Execute(t.Context(), &ExecutionRequest{
		Message:          "later trial",
		ModelID:          "test-model",
		EphemeralSession: true,
	})
	require.NoError(t, err)
	require.True(t, response.Success)
	require.False(t, engine.retired.Load())
	require.Equal(t, 2, constructions)
	currentClient, ok = engine.CopilotClient().(*startupClient)
	require.True(t, ok)
	require.Same(t, secondClient, currentClient.CopilotClient)

	close(releaseHealthy)
	response, err = <-healthyDone, <-healthyErr
	require.NoError(t, err)
	require.True(t, response.Success)
	close(releaseSend)
	require.NoError(t, engine.Shutdown(context.Background()))
	require.NoError(t, ShutdownSharedClient(context.Background()))
}

func TestSendAndWaitBoundedReturnsCancellationAfterLateSuccess(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 20 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	release := make(chan struct{})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-release
			return &copilot.SessionEvent{}, nil
		},
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	event, abandoned, retire, err := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		close(release)
	})

	require.NotNil(t, event)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, abandoned)
	require.False(t, retire)
}

func TestDisconnectBoundedForceStopsBlockingDisconnect(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	release := make(chan struct{})
	session.EXPECT().Disconnect().DoAndReturn(func() error {
		<-release
		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	forced := false
	abandoned, retire, err := disconnectBounded(ctx, session, func() {
		forced = true
		close(release)
	})

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, forced)
	require.False(t, abandoned)
	require.False(t, retire)
}

func TestSendAndWaitBoundedDoesNotWaitForBlockedForceStop(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	releaseSend := make(chan struct{})
	releaseForce := make(chan struct{})
	t.Cleanup(func() {
		close(releaseSend)
		close(releaseForce)
	})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-releaseSend
			return nil, nil
		},
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	_, abandoned, retire, err := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		<-releaseForce
	})

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, abandoned)
	require.True(t, retire)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}

func TestDisconnectBoundedDoesNotWaitForBlockedForceStop(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	session := NewMockCopilotSession(gomock.NewController(t))
	releaseDisconnect := make(chan struct{})
	releaseForce := make(chan struct{})
	t.Cleanup(func() {
		close(releaseDisconnect)
		close(releaseForce)
	})
	session.EXPECT().Disconnect().DoAndReturn(func() error {
		<-releaseDisconnect
		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	abandoned, retire, err := disconnectBounded(ctx, session, func() {
		<-releaseForce
	})

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, abandoned)
	require.True(t, retire)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}

func TestExecuteRetiresClientWhenForceStopRemainsBlocked(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("retired-session")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sendStarted := make(chan struct{})
	releaseSend := make(chan struct{})
	releaseForce := make(chan struct{})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			close(sendStarted)
			<-releaseSend
			return nil, nil
		},
	)
	client := &forceStopTestClient{
		MockCopilotClient: clientMock,
		forceStop:         func() { <-releaseForce },
	}
	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan *ExecutionResponse, 1)
	errs := make(chan error, 1)
	go func() {
		response, err := engine.Execute(ctx, &ExecutionRequest{
			Message:          "blocked force stop",
			ModelID:          "test-model",
			EphemeralSession: true,
		})
		done <- response
		errs <- err
	}()
	<-sendStarted
	cancel()
	response, err := <-done, <-errs
	require.NoError(t, err)
	require.False(t, response.Success)
	require.True(t, engine.retired.Load())

	_, err = engine.Execute(t.Context(), &ExecutionRequest{Message: "later trial"})
	require.ErrorContains(t, err, "retired after unresponsive cleanup")

	close(releaseForce)
	close(releaseSend)
	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestExecuteBoundsDisconnectAfterCanceledSend(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 50 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1").Return(nil)

	releaseDisconnect := make(chan struct{})
	session.EXPECT().SessionID().Return("session-1")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	session.EXPECT().Disconnect().DoAndReturn(func() error {
		<-releaseDisconnect
		return nil
	})
	client := &forceStopTestClient{
		MockCopilotClient: clientMock,
		forceStop:         func() { close(releaseDisconnect) },
	}

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	response, err := engine.Execute(ctx, &ExecutionRequest{
		Message:          "timeout",
		ModelID:          "test-model",
		EphemeralSession: true,
	})

	require.NoError(t, err)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.Contains(t, response.ErrorMsg, "trial timed out")
	require.False(t, response.Success)
}

func TestExecuteBoundsUsageCollectionAfterCanceledSend(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := NewMockCopilotSession(ctrl)
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	client.EXPECT().DeleteSession(gomock.Any(), "session-1").Return(nil)
	session.EXPECT().SessionID().Return("session-1")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	session.EXPECT().UsageMetrics(gomock.Any()).DoAndReturn(
		func(ctx context.Context) (*rpc.UsageGetMetricsResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	session.EXPECT().ShutdownUsage(gomock.Any()).DoAndReturn(
		func(ctx context.Context) (*copilot.SessionShutdownData, error) {
			return nil, ctx.Err()
		},
	)
	session.EXPECT().Disconnect().Return(nil)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	response, err := engine.Execute(ctx, &ExecutionRequest{
		Message:          "timeout",
		ModelID:          "test-model",
		EphemeralSession: true,
	})

	require.NoError(t, err)
	require.Less(t, time.Since(start), 250*time.Millisecond)
	require.Contains(t, response.ErrorMsg, "trial timed out")
	require.Nil(t, response.Usage)
}

func TestExecuteShortensUsageBudgetWhenCancellationArrivesLate(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := NewMockCopilotSession(ctrl)
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("session-late-cancel")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	metricsStarted := make(chan struct{})
	session.EXPECT().UsageMetrics(gomock.Any()).DoAndReturn(
		func(ctx context.Context) (*rpc.UsageGetMetricsResult, error) {
			close(metricsStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	session.EXPECT().ShutdownUsage(gomock.Any()).Return(nil, nil)
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "session-late-cancel").Return(nil)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan *ExecutionResponse, 1)
	go func() {
		response, err := engine.Execute(ctx, &ExecutionRequest{
			Message: "late cancellation",
			ModelID: "test-model",
		})
		require.NoError(t, err)
		done <- response
	}()

	<-metricsStarted
	start := time.Now()
	cancel()
	response := <-done

	require.Less(t, time.Since(start), 250*time.Millisecond)
	require.False(t, response.Success)
	require.Contains(t, response.ErrorMsg, "trial canceled during cleanup")
}

func TestExecuteReportsDeadlineExceededByBlockingDisconnect(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
	t.Cleanup(func() { sendCancellationGrace = oldGrace })

	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("session-blocked-disconnect")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	releaseDisconnect := make(chan struct{})
	session.EXPECT().Disconnect().DoAndReturn(func() error {
		<-releaseDisconnect
		return nil
	})
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-blocked-disconnect").Return(nil)
	client := &forceStopTestClient{
		MockCopilotClient: clientMock,
		forceStop:         func() { close(releaseDisconnect) },
	}

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	response, err := engine.Execute(ctx, &ExecutionRequest{
		Message: "successful send",
		ModelID: "test-model",
	})

	require.NoError(t, err)
	require.False(t, response.Success)
	require.Contains(t, response.ErrorMsg, "session teardown failed")
	require.Contains(t, response.ErrorMsg, "context deadline exceeded")
}

func TestExecutePreservesParentCancellationThroughSkillAndCleanup(t *testing.T) {
	for _, tt := range []struct {
		name        string
		skill       bool
		cancelStage string
	}{
		{name: "healthy"},
		{name: "skill invocation only", skill: true},
		{name: "skill send", skill: true, cancelStage: "send"},
		{name: "skill disconnect", skill: true, cancelStage: "disconnect"},
		{name: "ephemeral deletion", cancelStage: "delete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			session := newSessionMock(ctrl)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
			session.EXPECT().SessionID().Return("late-cancellation")
			var handlers []func(copilot.SessionEvent)
			session.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
				handlers = append(handlers, handler)
				return func() {}
			})
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
				func(sendCtx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
					if tt.skill {
						for _, handler := range handlers {
							handler(copilot.SessionEvent{Data: &copilot.SkillInvokedData{Name: "test-skill"}})
						}
						require.ErrorIs(t, sendCtx.Err(), context.Canceled)
					}
					if tt.cancelStage == "send" {
						cancel()
					}
					return &copilot.SessionEvent{}, sendCtx.Err()
				},
			)
			session.EXPECT().Disconnect().DoAndReturn(func() error {
				if tt.cancelStage == "disconnect" {
					cancel()
				}
				return nil
			})
			client.EXPECT().DeleteSession(gomock.Any(), "late-cancellation").DoAndReturn(
				func(context.Context, string) error {
					if tt.cancelStage == "delete" {
						cancel()
					}
					return nil
				},
			)
			engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
			}).Build()
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

			response, err := engine.Execute(ctx, &ExecutionRequest{
				Message:                 "late cancellation",
				CancelOnSkillInvocation: tt.skill,
				EphemeralSession:        true,
			})
			require.NoError(t, err)
			if tt.cancelStage == "" {
				require.NoError(t, ctx.Err())
				require.True(t, response.Success)
				require.Empty(t, response.ErrorMsg)
			} else {
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				require.False(t, response.Success)
				require.Contains(t, response.ErrorMsg, "context canceled")
			}
			if tt.skill {
				require.Len(t, response.SkillInvocations, 1)
			}
		})
	}
}

func TestExecuteReplacementWaitRespectsRequestCancellation(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")
	t.Setenv("COPILOT_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
	oldConstruct, oldEmbeddedPath := sharedConstruct, embeddedCLIPath
	t.Cleanup(func() {
		sharedConstruct = oldConstruct
		embeddedCLIPath = oldEmbeddedPath
	})
	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	ctrl := gomock.NewController(t)
	firstClient := newClientMock(ctrl)
	secondClient := NewMockCopilotClient(ctrl)
	secondClient.EXPECT().Stop().Return(nil)
	secondClient.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil)
	started, release := make(chan struct{}), make(chan struct{})
	secondClient.EXPECT().Start(gomock.Any()).DoAndReturn(func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return firstClient }
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	oldClient := engine.currentClient()
	engine.retireClient(oldClient)
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return secondClient }
	session := newSessionMock(ctrl)
	secondClient.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("replacement")
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	session.EXPECT().Disconnect().Return(nil)
	secondClient.EXPECT().DeleteSession(gomock.Any(), "replacement").Return(nil)

	type result struct {
		response *ExecutionResponse
		err      error
	}
	firstDone := make(chan result, 1)
	go func() {
		response, err := engine.Execute(t.Context(), &ExecutionRequest{Message: "start replacement", EphemeralSession: true})
		firstDone <- result{response, err}
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	waitingDone := make(chan error, 1)
	go func() {
		_, err := engine.Execute(ctx, &ExecutionRequest{Message: "deadline while waiting"})
		waitingDone <- err
	}()
	var waitingErr error
	returnedBeforeRelease := false
	select {
	case waitingErr = <-waitingDone:
		returnedBeforeRelease = true
	case <-time.After(250 * time.Millisecond):
	}
	retired := make(chan struct{})
	go func() {
		engine.retireClient(oldClient)
		close(retired)
	}()
	retiredBeforeRelease := false
	select {
	case <-retired:
		retiredBeforeRelease = true
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	first := <-firstDone
	if !returnedBeforeRelease {
		waitingErr = <-waitingDone
	}
	<-retired
	require.NoError(t, engine.Shutdown(context.Background()))
	require.NoError(t, ShutdownSharedClient(context.Background()))
	require.NoError(t, first.err)
	require.True(t, first.response.Success)
	require.ErrorIs(t, waitingErr, context.DeadlineExceeded)
	require.True(t, returnedBeforeRelease, "canceled Execute waited for another request's replacement startup")
	require.True(t, retiredBeforeRelease, "retirement waited for replacement startup")
	require.False(t, engine.retired.Load(), "retiring the old generation must not retire its replacement")
}
