package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCopilotEngine_Initialize(t *testing.T) {
	t.Attr("Issue", "https://github.com/github/copilot-sdk/issues/668")
	t.Skip("Skipping - passing a context to copilot.Start causes copilot CLI to exit")

	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.Initialize(ctx)
	require.Error(t, err) // looks like copilot not forwarding the context.Canceled error back to us but it does cancel
}

func TestCopilotEngine_SetupResources(t *testing.T) {
	workspaceDir := t.TempDir()

	err := setupWorkspaceResources(workspaceDir, []ResourceFile{{Path: "data.txt", Content: []byte("value")}})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(workspaceDir, "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "value", string(content))
}

func TestJoinStrings(t *testing.T) {
	assert.Equal(t, "", joinStrings(nil))
	assert.Equal(t, "abc", joinStrings([]string{"a", "b", "c"}))
}

// TestCopilotEngine_Execute_StartRespectsCallerContext verifies that a Start()
// call that blocks indefinitely is canceled by the caller context.
func TestCopilotEngine_Execute_StartRespectsCallerContext(t *testing.T) {
	t.Attr("Issue", "https://github.com/github/copilot-sdk/issues/668")
	t.Skip("Skipping - passing a context to copilot.Start causes copilot CLI to exit")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	// Simulate a Start() that blocks until its context is canceled (mimicking
	// the copilot SDK hanging on the JSON-RPC Ping during protocol negotiation).
	clientMock.EXPECT().Start(gomock.Any()).DoAndReturn(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := engine.Execute(ctx, &ExecutionRequest{
		Message: "hello",
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "copilot failed to start")
	// Must have returned within a reasonable multiple of the timeout.
	assert.Less(t, elapsed, 5*time.Second)
}

func TestCopilotEngine_Execute_CreateSessionError(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(nil, errors.New("session create failed"))

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	require.NoError(t, engine.Initialize(context.Background()))

	t.Cleanup(func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	})

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to create session")
}

func TestCopilotEngine_Execute_UsesCallerDeadline(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)

	expectedDeadline := time.Now().Add(time.Minute)
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ *copilot.SessionConfig) (CopilotSession, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.WithinDuration(t, expectedDeadline, deadline, time.Second)
			return nil, errors.New("session create failed")
		})

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	ctx, cancel := context.WithDeadline(context.Background(), expectedDeadline)
	defer cancel()
	resp, err := engine.Execute(ctx, &ExecutionRequest{Message: "hello"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestCopilotEngine_Execute_AlreadyExpiredContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	resp, err := engine.Execute(ctx, &ExecutionRequest{Message: "hello"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, resp)
}

func TestCopilotEngine_Execute_NoDefaultTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ *copilot.SessionConfig) (CopilotSession, error) {
			_, ok := ctx.Deadline()
			assert.False(t, ok)
			return nil, errors.New("session create failed")
		})

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestCopilotEngine_Execute_SendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
	sessionMock.EXPECT().SessionID().Return("session-1")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New("send failed"))
	sessionMock.EXPECT().Disconnect()

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	t.Cleanup(func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	})

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello"})
	require.NoError(t, err)

	require.False(t, resp.Success)
	require.Equal(t, "send failed", resp.ErrorMsg)
}

func TestCopilotEngine_DeleteSession_PropagatesRemoteError(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	// Empty session id is a no-op and must not touch the remote.
	require.NoError(t, engine.DeleteSession(context.Background(), ""))

	// A failed remote delete must surface as an error rather than being
	// swallowed, so local tracking is preserved for shutdown cleanup.
	clientMock.EXPECT().DeleteSession(gomock.Any(), "sess-err").Return(errors.New("remote boom"))
	err := engine.DeleteSession(context.Background(), "sess-err")
	require.ErrorContains(t, err, "remote boom")

	// A successful remote delete returns nil.
	clientMock.EXPECT().DeleteSession(gomock.Any(), "sess-ok").Return(nil)
	require.NoError(t, engine.DeleteSession(context.Background(), "sess-ok"))
}

func TestCopilotEngine_Execute_PassesGraderRequestOptionsAndDeletesEphemeralSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	tool := copilot.Tool{Name: "set_waza_grade_pass"}

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, cfg *copilot.SessionConfig) (CopilotSession, error) {
			require.Equal(t, "judge-model", cfg.Model)
			require.NotNil(t, cfg.Streaming)
			require.True(t, *cfg.Streaming)
			require.Empty(t, cfg.SkillDirectories)
			require.Equal(t, "set_waza_grade_pass", cfg.Tools[0].Name)
			require.NotEmpty(t, cfg.WorkingDirectory)
			return sessionMock, nil
		})
	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SessionID().Return("grader-session")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, opts copilot.MessageOptions) (*copilot.SessionEvent, error) {
			require.Equal(t, "grade this", opts.Prompt)
			require.Equal(t, string(MessageModeEnqueue), opts.Mode)
			return &copilot.SessionEvent{}, nil
		})
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "grader-session").Times(1)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		ModelID:              "judge-model",
		Message:              "grade this",
		Tools:                []copilot.Tool{tool},
		MessageMode:          MessageModeEnqueue,
		Streaming:            true,
		NoSkills:             true,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	})
	require.NoError(t, err)
	require.Equal(t, "grader-session", resp.SessionID)
	require.Nil(t, resp.WorkspaceFiles)
}

func TestCopilotEngine_Execute_ResumedEphemeralSessionIsNotDeletedOrTracked(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "existing-session", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, cfg *copilot.ResumeSessionConfig) (CopilotSession, error) {
			require.NotNil(t, cfg.Streaming)
			require.True(t, *cfg.Streaming)
			require.Empty(t, cfg.SkillDirectories)
			require.Equal(t, "judge-tool", cfg.Tools[0].Name)
			return sessionMock, nil
		})
	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SessionID().Return("existing-session")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().Disconnect()

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return clientMock
		},
	}).Build()
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	})

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:              "grade existing session",
		SessionID:            "existing-session",
		Tools:                []copilot.Tool{{Name: "judge-tool"}},
		Streaming:            true,
		NoSkills:             true,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	})
	require.NoError(t, err)
	require.Equal(t, "existing-session", resp.SessionID)
}

func TestCopilotEngine_Shutdown_StopsClientAndCleansWorkspaces(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	workspaceDir := t.TempDir()
	engine.workspaces = append(engine.workspaces, workspaceDir)

	clientMock.EXPECT().Stop().Times(1)
	err := engine.Shutdown(context.Background())
	require.NoError(t, err)

	_, err = os.Stat(workspaceDir)
	assert.True(t, os.IsNotExist(err))
}

// TestCopilotEngineBuilder_CLIArgsCarriesModel is a regression test for #262 /
// PR #263: when defaultModelID is set, the engine must pass
// "--model <defaultModelID>" through copilot.ClientOptions.CLIArgs so the
// embedded CLI honors the eval-configured model instead of the user's local
// settings.json or experiment-flight default. Without this startup override,
// SessionConfig.Model is silently ignored by the embedded CLI and evals run
// against the wrong model.
func TestCopilotEngineBuilder_CLIArgsCarriesModel(t *testing.T) {
	clearCustomProviderEnv(t)
	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	const defaultModelID = "claude-sonnet-4.5"

	var captured *copilot.ClientOptions
	_ = NewCopilotEngineBuilder(defaultModelID, &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			captured = clientOptions
			return clientMock
		},
	}).Build()

	require.NotNil(t, captured, "NewCopilotClient must receive non-nil ClientOptions")
	conn, ok := captured.Connection.(copilot.StdioConnection)
	require.True(t, ok, "Connection must be a copilot.StdioConnection")
	require.Equal(t, []string{"--model", defaultModelID}, conn.Args,
		"Connection.Args must carry --model <defaultModelID> so it overrides the user's local Copilot settings.json and experiment-flight defaults")
}

// TestCopilotEngineBuilder_CLIArgsEmptyWhenNoDefaultModel is a regression test
// asserting that no --model CLI startup arg is injected when no default model
// is configured. This preserves the embedded CLI's own fallback model selection
// (settings.json / experiment flights) for callers that explicitly opt out.
func TestCopilotEngineBuilder_CLIArgsEmptyWhenNoDefaultModel(t *testing.T) {
	clearCustomProviderEnv(t)
	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	var captured *copilot.ClientOptions
	_ = NewCopilotEngineBuilder("", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			captured = clientOptions
			return clientMock
		},
	}).Build()

	require.NotNil(t, captured, "NewCopilotClient must receive non-nil ClientOptions")
	conn, ok := captured.Connection.(copilot.StdioConnection)
	require.True(t, ok, "Connection must be a copilot.StdioConnection")
	require.Empty(t, conn.Args,
		"Connection.Args must be empty when no defaultModelID is provided so the embedded CLI can pick its own fallback")
}

// TestCopilotEngineBuilder_CLIArgsEmptyWhenCustomProvider is a regression test
// for #305: when a BYOK / custom provider is configured via environment
// variables, the embedded Copilot CLI must NOT receive --model at startup.
// The Copilot CLI validates the startup --model against the GitHub Copilot
// catalog before the per-session ProviderConfig is applied, so passing a
// provider-only model ID (e.g. "minimax-m2.7") causes startup to fail with
// `Model "..." is not available`. SessionConfig.Model + SessionConfig.Provider
// passed in Execute still select the right model against the custom provider.
func TestCopilotEngineBuilder_CLIArgsEmptyWhenCustomProvider(t *testing.T) {
	clearCustomProviderEnv(t)
	t.Setenv("COPILOT_BASE_URL", "https://api.example.com/v1")
	t.Setenv("COPILOT_PROVIDER", "openai")
	t.Setenv("COPILOT_API_KEY", "test-key")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	const defaultModelID = "minimax-m2.7"

	var captured *copilot.ClientOptions
	engine := NewCopilotEngineBuilder(defaultModelID, &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			captured = clientOptions
			return clientMock
		},
	}).Build()

	require.NotNil(t, captured, "NewCopilotClient must receive non-nil ClientOptions")
	conn, ok := captured.Connection.(copilot.StdioConnection)
	require.True(t, ok, "Connection must be a copilot.StdioConnection")
	require.Empty(t, conn.Args,
		"Connection.Args must be empty when a custom BYOK provider is configured so the embedded CLI does not pre-validate a provider-only model ID against the GitHub Copilot catalog (#305)")

	// The engine should still know the defaultModelID and provider for
	// per-session SessionConfig assembly.
	require.Equal(t, defaultModelID, engine.defaultModelID,
		"defaultModelID must still be retained on the engine so Execute can apply it via SessionConfig.Model")
	require.True(t, engine.provider.enabled(),
		"custom provider must be detected from env and retained for per-session ProviderConfig")
}

// clearCustomProviderEnv unsets every COPILOT_* variable that
// providerFromEnv() reads so the surrounding shell cannot influence
// CLIArgs assertions.
func clearCustomProviderEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL",
		"COPILOT_PROVIDER", "COPILOT_PROVIDER_TYPE",
		"COPILOT_WIRE_API", "COPILOT_PROVIDER_WIRE_API",
		"COPILOT_API_KEY", "COPILOT_PROVIDER_API_KEY",
		"COPILOT_BEARER_TOKEN", "COPILOT_PROVIDER_BEARER_TOKEN",
	} {
		name := name // capture loop variable
		prev, existed := os.LookupEnv(name)
		_ = os.Unsetenv(name)
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, prev)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}
