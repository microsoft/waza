package execution

import (
	"context"
	"fmt"
	"log/slog"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

// CopilotSession is just an interface over [*copilot.Session]
type CopilotSession interface {
	// Disconnect maps to [copilot.Session.Disconnect]. It closes the session and releases resources, however it
	// doesn't delete data and the session is still resumable until deleted via [copilot.Client.DeleteSession].
	Disconnect() error

	// On maps to [copilot.Session.On]
	On(handler copilot.SessionEventHandler) func()

	// SendAndWait maps to [copilot.Session.SendAndWait]
	SendAndWait(ctx context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error)

	// SessionID returns [copilot.Session.SessionID]
	SessionID() string

	UsageMetrics(ctx context.Context) (*rpc.UsageGetMetricsResult, error)

	ShutdownUsage(ctx context.Context) (*copilot.SessionShutdownData, error)
}

// CopilotClient is just an interface over [*copilot.Client]
type CopilotClient interface {
	// CreateSession maps to [copilot.Client.CreateSession]
	CreateSession(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error)

	// GetAuthStatus maps to [copilot.Client.GetAuthStatus]
	GetAuthStatus(ctx context.Context) (*copilot.GetAuthStatusResponse, error)

	// Start maps to [copilot.Client.Start]
	Start(ctx context.Context) error

	// Stop maps to [copilot.Client.Stop]
	Stop() error

	// ResumeSessionWithOptions maps to [copilot.Client.ResumeSessionWithOptions]
	ResumeSessionWithOptions(ctx context.Context, sessionID string, config *copilot.ResumeSessionConfig) (CopilotSession, error)

	// DeleteSession maps to [copilot.Client.DeleteSession]
	DeleteSession(ctx context.Context, sessionID string) error

	// ListModels maps to [copilot.Client.ListModels]
	ListModels(ctx context.Context) ([]copilot.ModelInfo, error)
}

func newCopilotClient(clientOptions *copilot.ClientOptions) CopilotClient {
	return &copilotClientWrapper{
		inner: copilot.NewClient(clientOptions),
	}
}

type copilotClientWrapper struct {
	inner *copilot.Client
}

func (w *copilotClientWrapper) CreateSession(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
	sess, err := w.inner.CreateSession(ctx, config)

	if err != nil {
		return nil, err
	}

	return &copilotSessionWrapper{inner: sess, client: w.inner}, nil
}

func (w *copilotClientWrapper) ResumeSessionWithOptions(ctx context.Context, sessionID string, config *copilot.ResumeSessionConfig) (CopilotSession, error) {
	sess, err := w.inner.ResumeSessionWithOptions(ctx, sessionID, config)

	if err != nil {
		return nil, err
	}

	return &copilotSessionWrapper{inner: sess, client: w.inner}, nil
}

func (w *copilotClientWrapper) Start(ctx context.Context) error {
	return w.inner.Start(ctx)
}

func (w *copilotClientWrapper) Stop() error {
	return w.inner.Stop()
}

func (w *copilotClientWrapper) GetAuthStatus(ctx context.Context) (*copilot.GetAuthStatusResponse, error) {
	return w.inner.GetAuthStatus(ctx)
}

func (w *copilotClientWrapper) DeleteSession(ctx context.Context, sessionID string) error {
	return w.inner.DeleteSession(ctx, sessionID)
}

func (w *copilotClientWrapper) ListModels(ctx context.Context) ([]copilot.ModelInfo, error) {
	return w.inner.ListModels(ctx)
}

// copilotSessionWrapper is a light wrapper that forwards all calls to [copilot.Session]
// and only has to exist because [copilot.Session.SessionID] is a field, so we can't represent
// it in an interface...
type copilotSessionWrapper struct {
	inner        *copilot.Session
	client       *copilot.Client
	disconnected bool
}

func (w *copilotSessionWrapper) Disconnect() error {
	w.disconnected = true
	return w.inner.Disconnect()
}

func (w *copilotSessionWrapper) On(handler copilot.SessionEventHandler) func() {
	return w.inner.On(handler)
}

func (w *copilotSessionWrapper) SendAndWait(ctx context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error) {
	return w.inner.SendAndWait(ctx, options)
}

func (w *copilotSessionWrapper) SessionID() string {
	return w.inner.SessionID
}

func (w *copilotSessionWrapper) UsageMetrics(ctx context.Context) (*rpc.UsageGetMetricsResult, error) {
	return w.inner.RPC.Usage.GetMetrics(ctx)
}

func (w *copilotSessionWrapper) ShutdownUsage(ctx context.Context) (*copilot.SessionShutdownData, error) {
	session := w.inner
	if w.disconnected {
		resumed, err := w.client.ResumeSessionWithOptions(ctx, session.SessionID, &copilot.ResumeSessionConfig{})
		if err != nil {
			return nil, err
		}
		session = resumed
		defer func() {
			if err := session.Disconnect(); err != nil {
				slog.Warn("failed to disconnect usage fallback session", "sessionID", session.SessionID, "error", err)
			}
		}()
	}
	if _, err := session.RPC.Shutdown(ctx, nil); err != nil {
		return nil, err
	}
	// Disconnect clears live handlers. Read the persisted shutdown event as
	// well so finalization works for previously detached/resumed sessions.
	events, err := session.GetEvents(ctx)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if data, ok := events[i].Data.(*copilot.SessionShutdownData); ok {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no final session.shutdown event reported")
}
