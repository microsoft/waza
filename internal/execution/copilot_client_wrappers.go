package execution

import (
	"context"
	"fmt"

	"github.com/Masterminds/semver/v3"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// CopilotSession is just an interface over [*copilot.Session]
type CopilotSession interface {
	// ConfigureSandbox enables Copilot's native sandbox and limits model-visible tools to the
	// isolated task workspace plus read-only skill directories.
	ConfigureSandbox(ctx context.Context, workspaceDir string, readonlyDirs []string, config models.SandboxConfig) error

	// Disconnect maps to [copilot.Session.Disconnect]. It closes the session and releases resources, however it
	// doesn't delete data and the session is still resumable until deleted via [copilot.Client.DeleteSession].
	Disconnect() error

	// On maps to [copilot.Session.On]
	On(handler copilot.SessionEventHandler) func()

	// SendAndWait maps to [copilot.Session.SendAndWait]
	SendAndWait(ctx context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error)

	// SessionID returns [copilot.Session.SessionID]
	SessionID() string
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

	return &copilotSessionWrapper{inner: sess, getStatus: w.inner.GetStatus}, nil
}

func (w *copilotClientWrapper) ResumeSessionWithOptions(ctx context.Context, sessionID string, config *copilot.ResumeSessionConfig) (CopilotSession, error) {
	sess, err := w.inner.ResumeSessionWithOptions(ctx, sessionID, config)

	if err != nil {
		return nil, err
	}

	return &copilotSessionWrapper{inner: sess, getStatus: w.inner.GetStatus}, nil
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
	inner     *copilot.Session
	getStatus func(context.Context) (*copilot.GetStatusResponse, error)
}

func (w *copilotSessionWrapper) checkSandboxRuntime(ctx context.Context) error {
	status, err := w.getStatus(ctx)
	if err != nil {
		return fmt.Errorf("verifying Copilot CLI sandbox support: %w", err)
	}
	const minimumVersion = "1.0.80"
	if status == nil {
		return fmt.Errorf("sandbox requires Copilot CLI %s or newer; runtime returned no version", minimumVersion)
	}
	version, err := semver.StrictNewVersion(status.Version)
	if err != nil {
		return fmt.Errorf("sandbox requires Copilot CLI %s or newer; cannot verify runtime version %q: %w", minimumVersion, status.Version, err)
	}
	if version.LessThan(semver.MustParse(minimumVersion)) {
		return fmt.Errorf("sandbox requires Copilot CLI %s or newer for native URL enforcement; got %s; upgrade COPILOT_CLI_PATH or unset it to use the bundled CLI", minimumVersion, status.Version)
	}
	return nil
}

func (w *copilotSessionWrapper) ConfigureSandbox(ctx context.Context, workspaceDir string, readonlyDirs []string, config models.SandboxConfig) error {
	if !config.Enabled {
		return nil
	}
	if err := w.checkSandboxRuntime(ctx); err != nil {
		return err
	}
	options, permissions, err := sessionSandboxConfiguration(workspaceDir, readonlyDirs, config)
	if err != nil {
		return err
	}
	updated, err := w.inner.RPC.Options.Update(ctx, options)
	if err != nil {
		return err
	}
	if !updated.Success {
		return fmt.Errorf("copilot rejected the sandbox configuration")
	}
	if permissions == nil {
		return nil
	}
	configured, err := w.inner.RPC.Permissions.Configure(ctx, permissions)
	if err != nil {
		return err
	}
	if !configured.Success {
		return fmt.Errorf("copilot rejected the workspace permission boundary")
	}
	return nil
}

func (w *copilotSessionWrapper) Disconnect() error {
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
