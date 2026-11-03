package execution

import (
	"context"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
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
	_, err, abandoned := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		releaseOnce.Do(func() { close(release) })
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, abandoned)
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
	_, err, abandoned := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {
		forced = true
	})

	require.ErrorIs(t, err, context.Canceled)
	require.True(t, forced)
	require.True(t, abandoned)
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
	go close(release)
	event, err, abandoned := sendAndWaitBounded(ctx, session, copilot.MessageOptions{}, func() {})

	require.NotNil(t, event)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, abandoned)
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
	err, abandoned := disconnectBounded(ctx, session, func() {
		forced = true
		close(release)
	})

	require.NoError(t, err)
	require.True(t, forced)
	require.False(t, abandoned)
}

func TestExecuteBoundsDisconnectAfterCanceledSend(t *testing.T) {
	oldGrace := sendCancellationGrace
	sendCancellationGrace = 5 * time.Millisecond
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
	require.Less(t, time.Since(start), 250*time.Millisecond)
	require.Contains(t, response.ErrorMsg, "trial timed out")
	require.False(t, response.Success)
}
