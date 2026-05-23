package execution

import (
	"context"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
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
		sharedClient = sharedConstruct(&copilot.ClientOptions{
			LogLevel:    logLevel,
			AutoStart:   utils.Ptr(false),
			AutoRestart: utils.Ptr(true),
		})
	})
	return sharedClient
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
