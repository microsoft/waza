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
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.Initialize(ctx)
	require.Error(t, err) // looks like copilot not forwarding the context.Canceled error back to us but it does cancel
}

func TestCopilotEngine_SetupResources(t *testing.T) {
	workspaceDir := t.TempDir()

	err := setupWorkspaceResources(workspaceDir, []ResourceFile{{Path: "data.txt", Content: "value"}})
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(workspaceDir, "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "value", string(content))
}

func TestJoinStrings(t *testing.T) {
	assert.Equal(t, "", joinStrings(nil))
	assert.Equal(t, "abc", joinStrings([]string{"a", "b", "c"}))
}

func TestCopilotEngine_Execute_CreateSessionError(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(nil, errors.New("session create failed"))

	engine := NewCopilotEngineBuilder("test", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient {
			return clientMock
		},
	}).Build()

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello", Timeout: time.Second})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to create session")
}

func TestCopilotEngine_Execute_SendError(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)
	sessionMock := NewMockcopilotSession(ctrl)

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)

	sessionMock.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
	sessionMock.EXPECT().SessionID().Return("session-1")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New("send failed"))

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient {
			return clientMock
		},
	}).Build()

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "hello", Timeout: time.Second})
	require.NoError(t, err)

	require.False(t, resp.Success)
	require.Equal(t, "send failed", resp.ErrorMsg)
}

func TestCopilotEngine_Shutdown_StopsClientAndCleansWorkspaces(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := NewMockcopilotClient(ctrl)

	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) copilotClient { return clientMock },
	}).Build()

	workspaceDir := t.TempDir()
	engine.workspaces = append(engine.workspaces, workspaceDir)

	clientMock.EXPECT().Stop().Times(1)
	err := engine.Shutdown(context.Background())
	require.NoError(t, err)

	_, err = os.Stat(workspaceDir)
	assert.True(t, os.IsNotExist(err))
}
