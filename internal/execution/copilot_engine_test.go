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

// TestCopilotEngine_Execute_StartRespectsTimeout verifies that a Start() call
// that blocks indefinitely is canceled by req.Timeout, preventing a deadlock.
func TestCopilotEngine_Execute_StartRespectsTimeout(t *testing.T) {
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

	start := time.Now()
	_, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
		Timeout: 50 * time.Millisecond,
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

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello", Timeout: time.Second})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to create session")
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

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello", Timeout: time.Second})
	require.NoError(t, err)

	require.False(t, resp.Success)
	require.Equal(t, "send failed", resp.ErrorMsg)
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
