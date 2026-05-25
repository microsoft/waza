package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/embedded"
	"github.com/microsoft/waza/internal/utils"
)

// SharedClientOptions configures the lazily-constructed process-wide Copilot
// SDK client returned by [SharedClient]. Only the first call wins; subsequent
// calls receive the already-built client regardless of options.
type SharedClientOptions struct {
	// LogLevel passed through to the underlying copilot.Client. Defaults to
	// "error" when blank.
	LogLevel string
}

var (
	sharedOnce      sync.Once
	sharedClient    CopilotClient
	sharedShutdown  sync.Once
	sharedErr       error
	sharedConstruct = newCopilotClient // overridable for tests
	embeddedCLIPath = embedded.Path    // overridable for tests
)

// SharedClient returns a lazily-constructed, process-wide [CopilotClient].
//
// Rationale (#135 R2): the embedded Copilot CLI process is expensive to
// spawn / tear down. Now that all per-call state (workdir, model, MCP
// servers, skill dirs, system message) is provided to CreateSession and
// ResumeSessionWithOptions, a single SDK client can serve every
// [CopilotEngine] (one per --model) and every grader within a `waza run`.
//
// The client is started lazily on first use by [CopilotEngine.Initialize] (or
// by an explicit [Start] caller) and is stopped exactly once via
// [ShutdownSharedClient]. Engines built on top of the shared client must not
// call client.Stop() themselves.
//
// Tests that need an isolated client can either construct one directly with
// [newCopilotClient] (package-private) or pass a custom NewCopilotClient
// factory via [CopilotEngineBuilderOptions].
func SharedClient(opts SharedClientOptions) CopilotClient {
	sharedOnce.Do(func() {
		logLevel := opts.LogLevel
		if logLevel == "" {
			logLevel = "error"
		}
		clientOptions, err := sharedClientOptions(logLevel)
		if err != nil {
			slog.Warn("Copilot CLI path resolution failed; refusing PATH fallback", "error", err)
			sharedClient = &startupErrorClient{err: err}
			return
		}
		sharedClient = sharedConstruct(clientOptions)
	})
	return sharedClient
}

func sharedClientOptions(logLevel string) (*copilot.ClientOptions, error) {
	opts := &copilot.ClientOptions{
		LogLevel:    logLevel,
		AutoStart:   utils.Ptr(false),
		AutoRestart: utils.Ptr(true),
	}

	if cliPath := os.Getenv("COPILOT_CLI_PATH"); cliPath != "" {
		info, err := os.Stat(cliPath)
		if err != nil {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: %w", cliPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: path is a directory", cliPath)
		}
		opts.CLIPath = cliPath
		slog.Info("using Copilot CLI", "source", "COPILOT_CLI_PATH", "path", cliPath)
		return opts, nil
	}

	cliPath, err := embeddedCLIPath()
	if err != nil {
		return nil, fmt.Errorf("embedded Copilot CLI is unavailable and COPILOT_CLI_PATH is not set; refusing to fall back to PATH: %w", err)
	}
	opts.CLIPath = cliPath
	slog.Info("using Copilot CLI", "source", "embedded", "path", cliPath)
	return opts, nil
}

type startupErrorClient struct {
	err error
}

func (c *startupErrorClient) CreateSession(context.Context, *copilot.SessionConfig) (CopilotSession, error) {
	return nil, c.err
}

func (c *startupErrorClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return nil, c.err
}

func (c *startupErrorClient) Start(context.Context) error {
	return c.err
}

func (c *startupErrorClient) Stop() error {
	return nil
}

func (c *startupErrorClient) ResumeSessionWithOptions(context.Context, string, *copilot.ResumeSessionConfig) (CopilotSession, error) {
	return nil, c.err
}

func (c *startupErrorClient) DeleteSession(context.Context, string) error {
	return c.err
}

func (c *startupErrorClient) ListModels(context.Context) ([]copilot.ModelInfo, error) {
	return nil, c.err
}

// ShutdownSharedClient stops the underlying Copilot SDK process if a shared
// client was ever constructed. Safe to call multiple times. Should be invoked
// once from the top-level command after all engines have been Shutdown and
// all graders have completed.
func ShutdownSharedClient(_ context.Context) error {
	if sharedClient == nil {
		return nil
	}
	sharedShutdown.Do(func() {
		sharedErr = sharedClient.Stop()
	})
	return sharedErr
}

// resetSharedClientForTest restores SharedClient to a pristine state. For
// tests only.
func resetSharedClientForTest() {
	sharedOnce = sync.Once{}
	sharedShutdown = sync.Once{}
	sharedClient = nil
	sharedErr = nil
}
