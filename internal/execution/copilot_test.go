package execution

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/sync/errgroup"
)

var enableCopilotTests = os.Getenv("ENABLE_COPILOT_TESTS") == "true"

func TestCopilotNoSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	const expectedModel = "this-model-wins"

	unregisterCount := 0
	unregister := func() { unregisterCount++ }

	sourceDir := t.TempDir()

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			OnPermissionRequest: allowAllTools,
			Model:               expectedModel,
			SkillDirectories:    []string{sourceDir},
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(unregister)
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err := engine.Initialize(ctx)
	require.NoError(t, err)

	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:   "hello?",
		ModelID:   "this-model-wins",
		SessionID: "", // ie, create a new session each time
		Timeout:   time.Minute,
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
	require.Equal(t, "session-1", resp.SessionID)
	require.Empty(t, resp.ErrorMsg)
	require.True(t, resp.Success)
	require.Equal(t, "this-model-wins", resp.ModelID)
	require.Equal(t, 1, unregisterCount) // only slog handler is unsubscribed; events collector stays alive for shutdown
}

func TestCopilotResumeSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	sourceDir, err := os.Getwd()
	require.NoError(t, err)

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.ResumeSessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-1", expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err = engine.Initialize(ctx)
	require.NoError(t, err)

	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:   "hello?",
		SessionID: "session-1",
		Timeout:   time.Minute,
	})
	require.NoError(t, err)
	require.Equal(t, "session-1", resp.SessionID)
	require.Empty(t, resp.ErrorMsg)
	require.True(t, resp.Success)
}

func TestCopilotSendAndWaitReturnsErrorInResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	sourceDir := t.TempDir()
	const sessionErrorMsg = "session error occurred"

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New(sessionErrorMsg))
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "message",
		Timeout:   time.Minute,
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
	require.Equal(t, sessionErrorMsg, resp.ErrorMsg)
}

func TestCopilotExecute_RequiredFields(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockcopilotClient(ctrl)
	// Start() should NOT be called when the request is invalid (e.g. Timeout == 0),
	// because extractReqParams now runs before startOnce.Do.

	builder := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient {
			return client
		},
	})
	engine := builder.Build()

	testCases := []struct {
		ER    ExecutionRequest
		Error string
	}{
		{ER: ExecutionRequest{Timeout: 0}, Error: "positive Timeout is required"},
	}

	for _, td := range testCases {
		t.Run("error: "+td.Error, func(t *testing.T) {
			resp, err := engine.Execute(context.Background(), &td.ER)
			require.ErrorContains(t, err, td.Error)
			require.Empty(t, resp)
		})
	}
}

func TestCopilotInitialize_PropagatesStartError(t *testing.T) {
	// Regression test: Initialize() must propagate Start() errors so callers see
	// copilot CLI startup failures instead of hanging or proceeding silently.
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)

	// Start returns an error, simulating a copilot CLI that fails to start.
	clientMock.EXPECT().Start(gomock.Any()).Return(errors.New("context canceled"))
	clientMock.EXPECT().Stop().AnyTimes()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()

	err := engine.Initialize(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "copilot failed to start")
}

func TestCopilotExecuteParallel(t *testing.T) {
	if !enableCopilotTests {
		t.Skip("ENABLE_COPILOT_TESTS must be set in order to run live copilot tests")
	}

	for range 5 {
		engine := NewCopilotEngineBuilder("gpt-4o-mini", nil).Build()

		err := engine.Initialize(context.Background())
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		eg := errgroup.Group{}

		for range 10 {
			eg.Go(func() error {
				_, err := engine.Execute(ctx, &ExecutionRequest{
					Message: "hello!",
					Timeout: 30 * time.Second,
				})
				return err
			})
		}

		err = eg.Wait()
		require.NoError(t, err)
		require.NoError(t, engine.Shutdown(context.Background()))
	}
}

type sessionConfigMatcher struct {
	expected  any
	sourceDir string
	t         *testing.T
}

func (m sessionConfigMatcher) Matches(x any) bool {
	switch tempC := x.(type) {
	case *copilot.SessionConfig:
		c := *tempC
		expected, ok := m.expected.(copilot.SessionConfig)
		require.True(m.t, ok)

		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)

		if expected.OnPermissionRequest == nil {
			require.Nil(m.t, c.OnPermissionRequest)
		} else {
			require.NotNil(m.t, c.OnPermissionRequest)
		}

		c.WorkingDirectory = ""

		// Equal can't compare function ptrs..
		expected.OnPermissionRequest = nil
		c.OnPermissionRequest = nil

		require.Equal(m.t, expected, c)
	case *copilot.ResumeSessionConfig:
		c := *tempC
		expected, ok := m.expected.(copilot.ResumeSessionConfig)
		require.True(m.t, ok)

		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)

		if expected.OnPermissionRequest == nil {
			require.Nil(m.t, c.OnPermissionRequest)
		} else {
			require.NotNil(m.t, c.OnPermissionRequest)
		}

		c.WorkingDirectory = ""

		// Equal can't compare function ptrs..
		expected.OnPermissionRequest = nil
		c.OnPermissionRequest = nil

		require.Equal(m.t, expected, c)
	default:
		require.FailNow(m.t, "Unhandled session configuration type %T", tempC)
	}

	return true
}

func (m sessionConfigMatcher) String() string {
	return ""
}
