package execution

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/embedded"
)

// SharedClientOptions configures a lazily-constructed process-wide Copilot SDK
// client returned by [SharedClient]. Clients are shared by startup-compatibility
// key, including the effective environment and authentication for sandboxed
// clients. Within each key, the first LogLevel wins.
type SharedClientOptions struct {
	// LogLevel passed through to the underlying copilot.Client. Defaults to
	// "error" when blank.
	LogLevel string
	// CLIArgs passed through to the underlying copilot.Client. Calls with the
	// same CLIArgs and compatible environment share one process; calls with
	// different CLIArgs get separate processes because CLIArgs are startup-only.
	CLIArgs []string
	// SanitizeEnvironment gives the Copilot CLI process only the operational
	// variables needed by a sandboxed evaluation. It is part of the client key
	// because a process environment cannot vary between sessions.
	SanitizeEnvironment bool
}

var (
	sharedMu        sync.Mutex
	sharedClients   map[string]CopilotClient
	sharedRetired   []CopilotClient
	sharedClosed    bool
	sharedShutdown  sync.Once
	sharedErr       error
	sharedConstruct = newCopilotClient // overridable for tests
	embeddedCLIPath = embedded.Path    // overridable for tests
)

var sharedClientShutdownTimeout = 30 * time.Second

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
	if !opts.SanitizeEnvironment {
		if client, ok := reusableSharedClient(key); ok {
			return client
		}
	}

	logLevel := opts.LogLevel
	if logLevel == "" {
		logLevel = "error"
	}
	clientOptions, err := sharedClientOptions(logLevel, opts.CLIArgs, opts.SanitizeEnvironment)
	if err != nil {
		slog.Warn("Copilot CLI path resolution failed; refusing PATH fallback", "error", err)
		client := &startupErrorClient{err: err}
		if !opts.SanitizeEnvironment {
			sharedClients[key] = client
		}
		return client
	}
	if opts.SanitizeEnvironment {
		conn, ok := clientOptions.Connection.(copilot.StdioConnection)
		if !ok {
			err := fmt.Errorf("sandboxed shared client requires a stdio connection, got %T", clientOptions.Connection)
			slog.Error("invalid sandboxed Copilot client configuration", "error", err)
			return &startupErrorClient{err: err}
		}
		environ := slices.Clone(conn.Env)
		slices.Sort(environ)
		// Hash the exact startup snapshot passed to the SDK, never raw secrets
		// or a second read of the host environment.
		fingerprint := sha256.Sum256(fmt.Appendf(nil, "%q\x00%q\x00%q", conn.Path, environ, clientOptions.GitHubToken))
		key += fmt.Sprintf("\x00%x", fingerprint)
		if client, ok := reusableSharedClient(key); ok {
			return client
		}
	}
	sharedClients[key] = manageStartup(sharedConstruct(clientOptions))
	return sharedClients[key]
}

// reusableSharedClient returns the client cached under key unless its startup
// failed in a replaceable way, in which case the entry is dropped. Callers
// must hold sharedMu.
func reusableSharedClient(key string) (CopilotClient, bool) {
	client := sharedClients[key]
	if client == nil {
		return nil, false
	}
	if managed, ok := client.(*startupClient); !ok || !managed.replaceable() {
		return client, true
	}
	delete(sharedClients, key)
	return nil, false
}

func retireSharedClient(client CopilotClient) {
	managed, ok := client.(*startupClient)
	if !ok {
		return
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	for key, current := range sharedClients {
		if current == managed {
			delete(sharedClients, key)
			sharedRetired = append(sharedRetired, managed)
			return
		}
	}
}

func sharedClientKey(cliArgs []string, sanitizeEnvironment bool) string {
	return fmt.Sprintf("%t\x00%s", sanitizeEnvironment, strings.Join(cliArgs, "\x00"))
}

func sharedClientOptions(logLevel string, cliArgs []string, sanitizeEnvironment bool) (*copilot.ClientOptions, error) {
	conn, err := clientConnection(cliArgs)
	if err != nil {
		return nil, err
	}
	if sanitizeEnvironment {
		// Sandboxed clients keep their explicit environment allowlist and never
		// receive the command-mock shim environment.
		return copilotClientOptions(logLevel, cliArgs, conn.Path, true), nil
	}
	// Client environments are startup-only; later unsandboxed task sessions may
	// create command mocks even when the first eval or grader does not use them.
	env, err := commandmock.RuntimeEnvironment()
	if err != nil {
		return nil, err
	}
	conn.Env = env
	return &copilot.ClientOptions{LogLevel: logLevel, Connection: conn}, nil
}

func clientOptions(logLevel string, cliArgs []string, sanitizeEnvironment bool) (*copilot.ClientOptions, error) {
	conn, err := clientConnection(cliArgs)
	if err != nil {
		return nil, err
	}
	if sanitizeEnvironment {
		return copilotClientOptions(logLevel, cliArgs, conn.Path, true), nil
	}
	return &copilot.ClientOptions{LogLevel: logLevel, Connection: conn}, nil
}

func clientConnection(cliArgs []string) (copilot.StdioConnection, error) {
	// SDK v1.0.0: CLIArgs/CLIPath/AutoStart/AutoRestart moved onto the
	// Connection (StdioConnection) or were removed. AutoStart/AutoRestart
	// are managed internally by the SDK now. StdioConnection is consumed
	// by value, so we set Path before assigning it to opts.Connection.
	conn := copilot.StdioConnection{
		Args: append([]string{}, cliArgs...),
		Env:  os.Environ(),
	}

	if cliPath := os.Getenv("COPILOT_CLI_PATH"); cliPath != "" {
		info, err := os.Stat(cliPath)
		if err != nil {
			return copilot.StdioConnection{}, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: %w", cliPath, err)
		}
		if info.IsDir() {
			return copilot.StdioConnection{}, fmt.Errorf("COPILOT_CLI_PATH %q is not usable: path is a directory", cliPath)
		}
		conn.Path = cliPath
		slog.Info("using Copilot CLI", "source", "COPILOT_CLI_PATH", "path", cliPath)
		return conn, nil
	}

	cliPath, err := embeddedCLIPath()
	if err != nil {
		return copilot.StdioConnection{}, fmt.Errorf("embedded Copilot CLI is unavailable and COPILOT_CLI_PATH is not set; refusing to fall back to PATH: %w", err)
	}
	conn.Path = cliPath
	slog.Info("using Copilot CLI", "source", "embedded", "path", cliPath)
	return conn, nil
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
	lowercaseProxyVariables := map[string]bool{"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true}

	result := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		allowedName := allowed[name] || lowercaseProxyVariables[name]
		if runtime.GOOS == "windows" {
			allowedName = allowed[upper]
		}
		if (upper == "TMPDIR" || upper == "TEMP" || upper == "TMP") && !validTemporaryDirectory(value) {
			continue
		}
		if allowedName && (!proxyVariables[upper] || safeProxyURL(value)) {
			result = append(result, entry)
		}
	}
	return result
}

func validTemporaryDirectory(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
func ShutdownSharedClient(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, sharedClientShutdownTimeout)
	defer cancel()

	sharedMu.Lock()
	clients := make([]CopilotClient, 0, len(sharedClients)+len(sharedRetired))
	for _, client := range sharedClients {
		clients = append(clients, client)
	}
	clients = append(clients, sharedRetired...)
	if len(clients) > 0 {
		sharedClosed = true
	}
	sharedMu.Unlock()

	if len(clients) == 0 {
		return nil
	}
	sharedShutdown.Do(func() {
		var errs []error
		stopErrs := make(chan error, len(clients))
		for _, client := range clients {
			go func(client CopilotClient) {
				stopErrs <- stopSharedClient(shutdownCtx, client)
			}(client)
		}
		for range clients {
			if err := <-stopErrs; err != nil {
				errs = append(errs, err)
			}
		}
		if err := commandmock.CloseRuntime(); err != nil {
			errs = append(errs, err)
		}
		sharedErr = errors.Join(errs...)
	})
	return sharedErr
}

func stopSharedClient(ctx context.Context, client CopilotClient) error {
	done := make(chan error, 1)
	go func() {
		done <- client.Stop()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	if force, ok := client.(interface{ ForceStop() }); ok {
		go force.ForceStop()
	}
	timer := time.NewTimer(startupCleanupTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return errors.Join(ctx.Err(), err)
	case <-timer.C:
		return errors.Join(ctx.Err(), errors.New("shared Copilot client cleanup exceeded force-stop grace"))
	}
}

// resetSharedClientForTest restores SharedClient to a pristine state. For
// tests only.
func resetSharedClientForTest() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	sharedShutdown = sync.Once{}
	sharedClients = nil
	sharedRetired = nil
	sharedClosed = false
	sharedErr = nil
}
