package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("WAZA_REJECT_COPILOT_PROCESS") == "1" {
		os.Exit(125)
	}
	os.Exit(m.Run())
}

func TestOwnedNativeClientDoesNotAllocateCommandMockRuntime(t *testing.T) {
	for _, phase := range []string{"shutdown", "invalid-path", "startup-failure"} {
		t.Run(phase, func(t *testing.T) {
			require.NoError(t, commandmock.CloseRuntime())
			t.Cleanup(func() { require.NoError(t, commandmock.CloseRuntime()) })
			directory := t.TempDir()
			t.Setenv("TMPDIR", directory)
			t.Setenv("TMP", directory)
			t.Setenv("TEMP", directory)
			path := filepath.Join(directory, "missing-copilot")
			if phase != "invalid-path" {
				// Unlike a shell script, the native test executable starts on Windows
				// too, then rejects the SDK's CLI flags before running any tests.
				if phase == "startup-failure" {
					t.Setenv("WAZA_REJECT_COPILOT_PROCESS", "1")
				}
				var err error
				path, err = os.Executable()
				require.NoError(t, err)
			}
			t.Setenv("COPILOT_CLI_PATH", path)
			engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{
				OwnClient: true, DisableCommandMocks: true,
				DiagnosticObserver: func(ExecutionDiagnostic) error { return nil },
			}).Build()
			if phase != "invalid-path" {
				require.IsType(t, &copilotClientWrapper{}, engine.client)
			}
			if phase == "invalid-path" {
				require.ErrorContains(t, engine.Initialize(t.Context()), "initialize/start_failed")
			}
			if phase == "startup-failure" {
				require.ErrorContains(t, engine.Initialize(t.Context()), "initialize/start_failed")
			}
			shutdownErr := engine.Shutdown(t.Context())
			if phase == "startup-failure" {
				if shutdownErr != nil {
					require.ErrorContains(t, shutdownErr, "client_stop/cleanup_failed")
				}
			} else {
				require.NoError(t, shutdownErr)
			}
			matches, err := filepath.Glob(filepath.Join(directory, "waza-command-mocks-*"))
			require.NoError(t, err)
			require.Empty(t, matches, "owned native clients must not allocate an unused global registry")
		})
	}
}

func TestDisableCommandMocksRequiresOwnedClient(t *testing.T) {
	engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{DisableCommandMocks: true}).Build()
	require.ErrorContains(t, engine.Initialize(t.Context()), "disabling command mocks requires an owned Copilot client")
	require.NoError(t, engine.Shutdown(t.Context()))
}

func TestOwnedClientRetainsCommandMocksByDefault(t *testing.T) {
	t.Cleanup(func() { require.NoError(t, commandmock.CloseRuntime()) })
	path := filepath.Join(t.TempDir(), "reject-copilot")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 125\n"), 0o700))
	t.Setenv("COPILOT_CLI_PATH", path)
	engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{OwnClient: true}).Build()
	defer func() { require.NoError(t, engine.Shutdown(t.Context())) }()
	session, err := engine.commandMockSession(t.TempDir(), &ExecutionRequest{
		CommandMocks: []models.CommandMockConfig{{
			Name: "az", Responses: []models.CommandMockResponse{{Args: []string{"account", "show"}}},
		}},
		CommandMocksBaseDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, session)
}

func TestDisableCommandMocksRejectsRequestsBeforeClientUse(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprintf("production=%t", owned), func(t *testing.T) {
			options := &CopilotEngineBuilderOptions{OwnClient: owned, DisableCommandMocks: true}
			stub := &stubClient{}
			if !owned {
				options.NewCopilotClient = func(*copilot.ClientOptions) CopilotClient { return stub }
			}
			engine := NewCopilotEngineBuilder("fixed-model", options).Build()
			response, err := engine.Execute(t.Context(), &ExecutionRequest{
				CommandMocks: []models.CommandMockConfig{{}},
			})
			require.EqualError(t, err, "command mocks are disabled for this Copilot engine")
			require.Nil(t, response)
			require.Empty(t, engine.workspaces)
			require.Empty(t, engine.commandMockSessions)
			require.NoError(t, engine.Shutdown(t.Context()))
		})
	}
}

func TestOwnedNativeShutdownPreservesSharedCommandMockRuntime(t *testing.T) {
	resetSharedClientForTest()
	require.NoError(t, commandmock.CloseRuntime())
	t.Cleanup(func() {
		require.NoError(t, commandmock.CloseRuntime())
		resetSharedClientForTest()
	})
	path := filepath.Join(t.TempDir(), "reject-copilot")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 125\n"), 0o700))
	t.Setenv("COPILOT_CLI_PATH", path)
	original := sharedConstruct
	t.Cleanup(func() { sharedConstruct = original })
	stub := &stubClient{}
	var registry string
	sharedConstruct = func(options *copilot.ClientOptions) CopilotClient {
		connection, ok := options.Connection.(copilot.StdioConnection)
		require.True(t, ok)
		for _, entry := range connection.Env {
			if value, ok := strings.CutPrefix(entry, "WAZA_COMMAND_MOCK_ROOT="); ok {
				registry = value
			}
		}
		return stub
	}
	require.Same(t, stub, SharedClient(SharedClientOptions{}))
	require.NotEmpty(t, registry)
	marker := filepath.Join(registry, "still-active")
	require.NoError(t, os.WriteFile(marker, []byte("shared client owns this registry"), 0o600))
	engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{OwnClient: true, DisableCommandMocks: true}).Build()
	require.NoError(t, engine.Shutdown(t.Context()))
	require.Zero(t, stub.stops)
	content, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "shared client owns this registry", string(content))
	require.NoError(t, ShutdownSharedClient(t.Context()))
	require.Equal(t, 1, stub.stops)
	_, err = os.Stat(registry)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCopilotEngineBuilderOwnedProductionClient(t *testing.T) {
	t.Run("fresh dedicated clients without runtime startup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "reject-copilot")
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 125\n"), 0o700))
		t.Setenv("COPILOT_CLI_PATH", path)
		first := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{OwnClient: true}).Build()
		second := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{OwnClient: true}).Build()
		require.True(t, first.ownsClient)
		require.True(t, second.ownsClient)
		require.IsType(t, &copilotClientWrapper{}, first.client)
		require.IsType(t, &copilotClientWrapper{}, second.client)
		require.NotSame(t, first.client, second.client)
		require.NoError(t, first.Shutdown(t.Context()))
		require.NoError(t, second.Shutdown(t.Context()))
	})
	t.Run("invalid explicit runtime never falls back", func(t *testing.T) {
		t.Setenv("COPILOT_CLI_PATH", filepath.Join(t.TempDir(), "missing-copilot"))
		engine := NewCopilotEngineBuilder("fixed-model", &CopilotEngineBuilderOptions{OwnClient: true}).Build()
		require.True(t, engine.ownsClient)
		require.IsType(t, &startupErrorClient{}, engine.client)
		require.ErrorContains(t, engine.Initialize(t.Context()), "COPILOT_CLI_PATH")
		require.NoError(t, engine.Shutdown(t.Context()))
	})
}
