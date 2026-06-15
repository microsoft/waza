package execution

import (
	"context"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// The first-event signal is open until the first event of any type arrives, then
// closed, and stays closed (idempotent) for every subsequent event.
func TestSessionEventsCollector_FirstEvent_ClosesOnFirstEvent(t *testing.T) {
	coll := NewSessionEventsCollector()

	select {
	case <-coll.FirstEvent():
		t.Fatal("FirstEvent must be open before any event is received")
	default:
	}

	coll.On(copilot.SessionEvent{Data: &copilot.SkillInvokedData{Name: "s", Path: "p"}})

	select {
	case <-coll.FirstEvent():
	default:
		t.Fatal("FirstEvent must be closed after the first event")
	}

	// A second event must not re-close the channel (would panic without the once).
	coll.On(copilot.SessionEvent{Data: &copilot.SkillInvokedData{Name: "s2", Path: "p2"}})

	select {
	case <-coll.FirstEvent():
	default:
		t.Fatal("FirstEvent must remain closed after subsequent events")
	}
}

// A session that launches but never produces a first event is a session-start
// hang. The first-event watchdog must abort it WELL before the (necessarily
// large) overall turn deadline and surface a distinct, actionable error.
func TestCopilotExecute_FirstEventTimeout_AbortsSessionStartHang(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), gomock.Any()).AnyTimes()
	sessionMock.EXPECT().SessionID().Return("session-first-event").AnyTimes()
	sessionMock.EXPECT().Disconnect().AnyTimes()
	sessionMock.EXPECT().On(gomock.Any()).AnyTimes().Return(func() {})

	// SendAndWait emits no event — it just blocks until its context is canceled
	// (which the first-event watchdog must do) and returns that error.
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(_ *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
	require.NoError(t, engine.Initialize(context.Background()))

	// Overall deadline is generous (30s); the first-event budget is tiny (50ms).
	// A working watchdog returns in well under a second.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:           "hello?",
		SourceDir:         sourceDir,
		FirstEventTimeout: 50 * time.Millisecond,
	})
	elapsed := time.Since(start)

	require.NoError(t, err) // the failure is reported in the response, not returned
	require.False(t, resp.Success)
	require.Contains(t, resp.ErrorMsg, "session start timeout")
	require.Contains(t, resp.ErrorMsg, "no first turn")
	require.Less(t, elapsed, 5*time.Second,
		"first-event watchdog must fire near its 50ms budget, not the 30s overall deadline (took %s)", elapsed)
}

// When the first event arrives within the budget, the watchdog disarms and the
// run completes normally — the overall deadline (not the first-event budget)
// governs the rest of the turn. This guards against false-aborting a slow-to-
// start but live first turn.
func TestCopilotExecute_FirstEventTimeout_DisarmsOnFirstEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), gomock.Any()).AnyTimes()
	sessionMock.EXPECT().SessionID().Return("session-first-event-ok").AnyTimes()
	sessionMock.EXPECT().Disconnect().AnyTimes()

	var handlers []func(copilot.SessionEvent)
	sessionMock.EXPECT().On(gomock.Any()).AnyTimes().DoAndReturn(func(h func(copilot.SessionEvent)) func() {
		handlers = append(handlers, h)
		return func() {}
	})

	// Emit a first event right away (disarms the watchdog), then complete
	// successfully — NOT with a context error.
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			require.NotEmpty(t, handlers)
			for _, h := range handlers {
				h(copilot.SessionEvent{Data: &copilot.SkillInvokedData{Name: "s", Path: "p"}})
			}
			return &copilot.SessionEvent{}, nil
		},
	)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(_ *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
	require.NoError(t, engine.Initialize(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:           "hello?",
		SourceDir:         sourceDir,
		FirstEventTimeout: 10 * time.Second,
	})

	require.NoError(t, err)
	require.True(t, resp.Success)
	require.False(t, strings.Contains(resp.ErrorMsg, "session start timeout"),
		"a run that produced a first event must not be reported as a session-start hang")
}
