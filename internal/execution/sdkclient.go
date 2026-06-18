package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/embedded"
)

// SharedClientOptions configures a lazily-constructed process-wide Copilot SDK
// client returned by [SharedClient]. Clients are shared by CLIArgs key; within
// each key, only the first call wins and subsequent calls receive the
// already-built client regardless of options.
type SharedClientOptions struct {
	// LogLevel passed through to the underlying copilot.Client. Defaults to
	// "error" when blank.
	LogLevel string
	// CLIArgs passed through to the underlying copilot.Client. Calls with the
	// same CLIArgs share one process; calls with different CLIArgs get separate
	// processes because CLIArgs are startup-only.
	CLIArgs []string
}

var (
	sharedMu        sync.Mutex
	sharedClients   map[string]CopilotClient
	sharedClosed    bool
	sharedShutdown  sync.Once
	sharedErr       error
	sharedConstruct = newCopilotClient // overridable for tests
	embeddedCLIPath = embedded.Path    // overridable for tests
)

var errSharedClientClosed = errors.New("shared Copilot client has been shut down")

// SharedClient returns a lazily-constructed, process-wide [CopilotClient].
//
// Rationale (#135 R2): the embedded Copilot CLI process is expensive to
// spawn / tear down. Now that all per-call state (workdir, model, MCP
// servers, skill dirs, system message) is provided to CreateSession and
// ResumeSessionWithOptions, a single SDK client can serve compatible
// [CopilotEngine] instances and graders within a `waza run`. Startup-only
// CLIArgs (for example --model) are part of the compatibility key.
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
	key := sharedClientKey(opts.CLIArgs)

	sharedMu.Lock()
	defer sharedMu.Unlock()

	if sharedClients == nil {
		sharedClients = make(map[string]CopilotClient)
	}
	if sharedClosed {
		return &startupErrorClient{err: errSharedClientClosed}
	}
	if client := sharedClients[key]; client != nil {
		return client
	}

	logLevel := opts.LogLevel
	if logLevel == "" {
		logLevel = "error"
	}
	clientOptions, err := sharedClientOptions(logLevel, opts.CLIArgs)
	if err != nil {
		slog.Warn("Copilot CLI path resolution failed; refusing PATH fallback", "error", err)
		sharedClients[key] = &startupErrorClient{err: err}
		return sharedClients[key]
	}
	sharedClients[key] = sharedConstruct(clientOptions)
	return sharedClients[key]
}

func sharedClientKey(cliArgs []string) string {
	return strings.Join(cliArgs, "\x00")
}

func sharedClientOptions(logLevel string, cliArgs []string) (*copilot.ClientOptions, error) {
	// SDK v1.0.0: CLIArgs/CLIPath/AutoStart/AutoRestart moved onto the
	// Connection (StdioConnection) or were removed. AutoStart/AutoRestart
	// are managed internally by the SDK now. StdioConnection is consumed
	// by value, so we set Path before assigning it to opts.Connection.
	conn := copilot.StdioConnection{
		Args: append([]string{}, cliArgs...),
	}

	if cliPath := os.Getenv("COPILOT_CLI_PATH"); cliPath != "" {
		info, err := os.Stat(cliPath)
		if err != nil {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: %w", cliPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: path is a directory", cliPath)
		}
		conn.Path = cliPath
		slog.Info("using Copilot CLI", "source", "COPILOT_CLI_PATH", "path", cliPath)
		return &copilot.ClientOptions{
			LogLevel:   logLevel,
			Connection: conn,
		}, nil
	}

	cliPath, err := embeddedCLIPath()
	if err != nil {
		return nil, fmt.Errorf("embedded Copilot CLI is unavailable and COPILOT_CLI_PATH is not set; refusing to fall back to PATH: %w", err)
	}
	conn.Path = cliPath
	slog.Info("using Copilot CLI", "source", "embedded", "path", cliPath)
	return &copilot.ClientOptions{
		LogLevel:   logLevel,
		Connection: conn,
	}, nil
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
	sharedMu.Lock()
	clients := make([]CopilotClient, 0, len(sharedClients))
	for _, client := range sharedClients {
		clients = append(clients, client)
	}
	if len(clients) > 0 {
		sharedClosed = true
	}
	sharedMu.Unlock()

	if len(clients) == 0 {
		return nil
	}
	sharedShutdown.Do(func() {
		var errs []error
		for _, client := range clients {
			if err := client.Stop(); err != nil {
				errs = append(errs, err)
			}
		}
		sharedErr = errors.Join(errs...)
	})
	return sharedErr
}

// resetSharedClientForTest restores SharedClient to a pristine state. For
// tests only.
func resetSharedClientForTest() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	sharedShutdown = sync.Once{}
	sharedClients = nil
	sharedClosed = false
	sharedErr = nil
}
