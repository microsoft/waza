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
