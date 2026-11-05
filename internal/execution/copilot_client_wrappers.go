package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/Masterminds/semver/v3"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
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
	wrapper := &copilotClientWrapper{inner: copilot.NewClient(clientOptions)}
	if clientOptions != nil {
		if conn, ok := clientOptions.Connection.(copilot.StdioConnection); ok {
			wrapper.processEnv = slices.Clone(conn.Env)
		}
	}
	return wrapper
}

// NewOwnedCopilotClient constructs a dedicated SDK client. Supply this factory
// through CopilotEngineBuilderOptions.NewCopilotClient to avoid SharedClient;
// the engine then owns and stops the client.
func NewOwnedCopilotClient(clientOptions *copilot.ClientOptions) CopilotClient {
	return newCopilotClient(clientOptions)
}

type copilotClientWrapper struct {
	inner *copilot.Client
	// processEnv is the environment the CLI process starts with; sandbox
	// policies deny its temporary roots even if the host environment changes.
	processEnv []string
}

func (w *copilotClientWrapper) CreateSession(ctx context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
	sess, err := w.inner.CreateSession(ctx, config)

	if err != nil {
		return nil, err
	}

	return &copilotSessionWrapper{inner: sess, client: w.inner, getStatus: w.inner.GetStatus, processEnv: w.processEnv}, nil
}

func (w *copilotClientWrapper) ResumeSessionWithOptions(ctx context.Context, sessionID string, config *copilot.ResumeSessionConfig) (CopilotSession, error) {
	sess, err := w.inner.ResumeSessionWithOptions(ctx, sessionID, config)

	if err != nil {
		return nil, err
	}

	return &copilotSessionWrapper{inner: sess, client: w.inner, getStatus: w.inner.GetStatus, processEnv: w.processEnv}, nil
}

func (w *copilotClientWrapper) Start(ctx context.Context) error {
	return w.inner.Start(ctx)
}

func (w *copilotClientWrapper) Stop() error {
	return w.inner.Stop()
}

func (w *copilotClientWrapper) ForceStop() {
	w.inner.ForceStop()
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
	getStatus    func(context.Context) (*copilot.GetStatusResponse, error)
	processEnv   []string
	disconnected bool
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
	options, permissions, err := sessionSandboxConfiguration(workspaceDir, readonlyDirs, config, w.processEnv)
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
	return w.shutdownUsage(ctx, nil, true)
}

func (w *copilotSessionWrapper) shutdownUsageDiagnostic(ctx context.Context, report func(DiagnosticCode), allowResume bool) (*copilot.SessionShutdownData, error) {
	return w.shutdownUsage(ctx, report, allowResume)
}

func (w *copilotSessionWrapper) shutdownUsage(ctx context.Context, report func(DiagnosticCode), allowResume bool) (data *copilot.SessionShutdownData, resultErr error) {
	fail := func(code DiagnosticCode, err error) error {
		if report == nil {
			return err
		}
		report(code)
		return &DiagnosticError{Diagnostic: ExecutionDiagnostic{Stage: StageShutdownUsage, Code: code}}
	}
	session := w.inner
	if w.disconnected {
		if !allowResume {
			return nil, fail(CodeUsageMissingOrPartial, fmt.Errorf("shutdown usage unavailable: isolated sessions cannot resume for usage fallback"))
		}
		resumed, err := w.client.ResumeSessionWithOptions(ctx, session.SessionID, &copilot.ResumeSessionConfig{})
		if err != nil {
			return nil, fail(CodeResume, err)
		}
		session = resumed
		defer func() {
			if err := session.Disconnect(); err != nil {
				if report == nil {
					slog.Warn("failed to disconnect usage fallback session", "sessionID", session.SessionID, "error", err)
				} else {
					resultErr = errors.Join(resultErr, fail(CodeFallbackDisconnect, err))
				}
			}
		}()
	}
	if _, err := session.RPC.Shutdown(ctx, nil); err != nil {
		return nil, fail(CodeShutdownRPC, err)
	}
	// Disconnect clears live handlers. Read the persisted shutdown event as
	// well so finalization works for previously detached/resumed sessions.
	events, err := session.GetEvents(ctx)
	if err != nil {
		return nil, fail(CodeHistory, err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if data, ok := events[i].Data.(*copilot.SessionShutdownData); ok {
			return data, nil
		}
	}
	return nil, fail(CodeUsageMissingOrPartial, fmt.Errorf("no final session.shutdown event reported"))
}
