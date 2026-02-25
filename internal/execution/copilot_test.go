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
			Model:            expectedModel,
			SkillDirectories: []string{sourceDir},
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(2).Return(unregister)
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient { return clientMock },
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
	require.Equal(t, unregisterCount, 2)
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
			Model:            "gpt-4o-mini",
			SkillDirectories: []string{sourceDir},
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-1", expectedConfig).Return(sessionMock, nil)
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(2).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient { return clientMock },
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
			Model:            "gpt-4o-mini",
			SkillDirectories: []string{sourceDir},
		},
	}

	clientMock.EXPECT().Start(gomock.Any())
	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	clientMock.EXPECT().Stop()

	sessionMock.EXPECT().On(gomock.Any()).Times(2).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New(sessionErrorMsg))
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient { return clientMock },
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
	client.EXPECT().Start(gomock.Any())

	builder := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient {
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

func TestCopilotExecuteParallel(t *testing.T) {
	if !enableCopilotTests {
		t.Skip("ENABLE_COPILOT_TESTS must be set in order to run live copilot tests")
	}

	for range 5 {
		engine := NewCopilotEngineBuilder("gpt-4o-mini", nil).Build()

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

		err := eg.Wait()
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
		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)
		c.WorkingDirectory = ""
		require.Equal(m.t, m.expected, c)
	case *copilot.ResumeSessionConfig:
		c := *tempC
		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)
		c.WorkingDirectory = ""
		require.Equal(m.t, m.expected, c)
	default:
		require.FailNow(m.t, "Unhandled session configuration type %T", tempC)
	}

	return true
}

func (m sessionConfigMatcher) String() string {
	return ""
}
