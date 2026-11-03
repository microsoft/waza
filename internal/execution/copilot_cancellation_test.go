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
	_, err, abandoned, retire := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
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
	forced := false
	_, err, abandoned, retire := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		forced = true
	})

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, forced)
	require.True(t, abandoned)
	require.True(t, retire)
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
	event, err, abandoned, retire := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
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
	err, abandoned, retire := disconnectBounded(ctx, session, func() {
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
	_, err, abandoned, retire := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
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
	err, abandoned, retire := disconnectBounded(ctx, session, func() {
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
