package execution

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockEngine_Initialize(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.Initialize(ctx)
	require.NoError(t, err)
}

func TestMockEngine_Execute_WritesResources(t *testing.T) {
	engine := NewMockEngine("test-model")

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
		Resources: []ResourceFile{{
			Path:    "fixtures/input.txt",
			Content: "test-content",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Contains(t, resp.FinalOutput, "Mock response for: hello")
	assert.Contains(t, resp.FinalOutput, "Analyzed 1 file(s)")

	content, err := os.ReadFile(filepath.Join(resp.WorkspaceDir, "fixtures", "input.txt"))
	require.NoError(t, err)
	assert.Equal(t, "test-content", string(content))

	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestMockEngine_Execute_ReplacesWorkspace(t *testing.T) {
	engine := NewMockEngine("test-model")

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	resp1, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "one"})
	require.NoError(t, err)
	firstWorkspace := resp1.WorkspaceDir

	resp2, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "two"})
	require.NoError(t, err)
	secondWorkspace := resp2.WorkspaceDir

	assert.NotEqual(t, firstWorkspace, secondWorkspace)
	_, statErr := os.Stat(firstWorkspace)
	assert.True(t, os.IsNotExist(statErr), "first workspace should be removed")

	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestMockEngine_Execute_SetupResourcesError(t *testing.T) {
	engine := NewMockEngine("test-model")

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	absPath := "/absolute/path.txt"
	if runtime.GOOS == "windows" {
		absPath = `C:\absolute\path.txt`
	}

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
		Resources: []ResourceFile{{
			Path:    absPath,
			Content: "x",
		}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to setup mock workspace resources")

	require.NoError(t, engine.Shutdown(context.Background()))
}
