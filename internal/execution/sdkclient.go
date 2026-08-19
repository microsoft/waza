package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/embedded"
)

// SharedClientOptions configures a lazily-constructed process-wide Copilot SDK
// client returned by [SharedClient]. Clients are shared by startup-compatibility
// key; within each key, only the first call wins and subsequent calls receive
// the already-built client regardless of options.
type SharedClientOptions struct {
	// LogLevel passed through to the underlying copilot.Client. Defaults to
	// "error" when blank.
	LogLevel string
	// CLIArgs passed through to the underlying copilot.Client. Calls with the
	// same CLIArgs share one process; calls with different CLIArgs get separate
	// processes because CLIArgs are startup-only.
	CLIArgs []string
	// SanitizeEnvironment gives the Copilot CLI process only the operational
	// variables needed by a sandboxed evaluation. It is part of the client key
	// because a process environment cannot vary between sessions.
	SanitizeEnvironment bool
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
	key := sharedClientKey(opts.CLIArgs, opts.SanitizeEnvironment)

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
	clientOptions, err := sharedClientOptions(logLevel, opts.CLIArgs, opts.SanitizeEnvironment)
	if err != nil {
		slog.Warn("Copilot CLI path resolution failed; refusing PATH fallback", "error", err)
		sharedClients[key] = &startupErrorClient{err: err}
		return sharedClients[key]
	}
	sharedClients[key] = sharedConstruct(clientOptions)
	return sharedClients[key]
}

func sharedClientKey(cliArgs []string, sanitizeEnvironment bool) string {
	return fmt.Sprintf("%t\x00%s", sanitizeEnvironment, strings.Join(cliArgs, "\x00"))
}

func sharedClientOptions(logLevel string, cliArgs []string, sanitizeEnvironment bool) (*copilot.ClientOptions, error) {
	if cliPath := os.Getenv("COPILOT_CLI_PATH"); cliPath != "" {
		info, err := os.Stat(cliPath)
		if err != nil {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: %w", cliPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: path is a directory", cliPath)
		}
		slog.Info("using Copilot CLI", "source", "COPILOT_CLI_PATH", "path", cliPath)
		return copilotClientOptions(logLevel, cliArgs, cliPath, sanitizeEnvironment), nil
	}

	cliPath, err := embeddedCLIPath()
	if err != nil {
		return nil, fmt.Errorf("embedded Copilot CLI is unavailable and COPILOT_CLI_PATH is not set; refusing to fall back to PATH: %w", err)
	}
	slog.Info("using Copilot CLI", "source", "embedded", "path", cliPath)
	return copilotClientOptions(logLevel, cliArgs, cliPath, sanitizeEnvironment), nil
}

func copilotClientOptions(logLevel string, cliArgs []string, cliPath string, sanitizeEnvironment bool) *copilot.ClientOptions {
	conn := copilot.StdioConnection{
		Path: cliPath,
		Args: append([]string{}, cliArgs...),
	}
	opts := &copilot.ClientOptions{
		LogLevel:   logLevel,
		Connection: conn,
	}
	if sanitizeEnvironment {
		// Environment belongs to the CLI process, not a session. Construct it at
		// this boundary so sandboxed clients cannot expose arbitrary Waza host
		// secrets to model-visible tools. GitHub authentication is passed through
		// the SDK's dedicated channel; persisted login remains available via HOME.
		conn.Env = sanitizedCLIEnv(os.Environ())
		opts.Connection = conn
		opts.GitHubToken = envFirst("COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN")
	}
	return opts
}

func sanitizedCLIEnv(environ []string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "USERNAME": true,
		"LOGNAME": true, "SHELL": true, "TERM": true, "COLORTERM": true,
		"TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true, "TZ": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
		"REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true, "NIX_SSL_CERT_FILE": true,
		"GIT_SSL_CAINFO": true, "COPILOT_HOME": true,
		"LC_ALL": true, "LC_COLLATE": true, "LC_CTYPE": true, "LC_MESSAGES": true,
		"LC_MONETARY": true, "LC_NUMERIC": true, "LC_TIME": true, "LC_PAPER": true,
		"LC_NAME": true, "LC_ADDRESS": true, "LC_TELEPHONE": true,
		"LC_MEASUREMENT": true, "LC_IDENTIFICATION": true,
		"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
		"XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true,
		"XDG_DATA_DIRS": true, "XDG_CONFIG_DIRS": true,
		"SYSTEMROOT": true, "COMSPEC": true, "PATHEXT": true, "USERPROFILE": true,
		"APPDATA": true, "LOCALAPPDATA": true,
	}
	proxyVariables := map[string]bool{"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true}

	result := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		if allowed[upper] && (!proxyVariables[upper] || safeProxyURL(value)) {
			result = append(result, entry)
		}
	}
	return result
}

func safeProxyURL(value string) bool {
	if strings.ContainsAny(value, "@?#") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.User == nil && (parsed.Path == "" || parsed.Path == "/")
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
