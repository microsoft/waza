package execution

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestMockEngine_Execute_ReturnsContextErrorBeforeWorkspace(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				return ctx, cancel
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.context()
			defer cancel()

			engine := NewMockEngine("test-model")
			require.NoError(t, engine.Initialize(context.Background()))

			resp, err := engine.Execute(ctx, &ExecutionRequest{
				Message:   "hello",
				Resources: []ResourceFile{{Path: "input.txt", Content: []byte("data")}},
			})

			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, resp)
			assert.Empty(t, engine.workspace, "workspace should not be created for a pre-canceled context")
		})
	}
}

func TestMockEngine_Execute_WritesResources(t *testing.T) {
	engine := NewMockEngine("test-model")

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
		Resources: []ResourceFile{{
			Path:    "fixtures/input.txt",
			Content: []byte("test-content"),
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.Contains(t, resp.FinalOutput, "Mock response for: hello")
	assert.Contains(t, resp.FinalOutput, "Analyzed 1 file(s):")
	assert.Contains(t, resp.FinalOutput, "test-content")

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
			Content: []byte("x"),
		}},
	})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to setup mock workspace resources")

	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestMockEngine_Execute_IncludesResourceContent(t *testing.T) {
	engine := NewMockEngine("test-model")
	require.NoError(t, engine.Initialize(context.Background()))

	jsContent := "async function fetchUser(id) {\n  const resp = await fetch(`/api/users/${id}`);\n  return resp.json();\n}"

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "What does this code do?",
		Resources: []ResourceFile{
			{Path: "fetch_user.js", Content: []byte(jsContent)},
			{Path: "empty.txt", Content: nil},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// File content should appear in output so output_contains graders can match
	assert.Contains(t, resp.FinalOutput, "async function fetchUser")
	assert.Contains(t, resp.FinalOutput, "await fetch")
	assert.Contains(t, resp.FinalOutput, "fetch_user.js")
	assert.Contains(t, resp.FinalOutput, "Analyzed 2 file(s):")
	// Empty file should still list path but no content
	assert.Contains(t, resp.FinalOutput, "empty.txt")

	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestMockEngine_Execute_TruncatesLargeContent(t *testing.T) {
	engine := NewMockEngine("test-model")
	require.NoError(t, engine.Initialize(context.Background()))

	largeContent := make([]byte, 2048)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "analyze",
		Resources: []ResourceFile{{Path: "big.txt", Content: largeContent}},
	})
	require.NoError(t, err)
	assert.Contains(t, resp.FinalOutput, "...(truncated)")

	require.NoError(t, engine.Shutdown(context.Background()))
}
