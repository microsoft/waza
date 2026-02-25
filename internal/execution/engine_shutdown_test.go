package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SpyEngine wraps an AgentEngine and tracks Shutdown calls.
// Exported so cmd/waza tests can use it if needed.
type SpyEngine struct {
	Inner         AgentEngine
	ShutdownCount atomic.Int32
	ShutdownErr   error // error to return from Shutdown
}

func NewSpyEngine(inner AgentEngine) *SpyEngine {
	return &SpyEngine{Inner: inner}
}

func (s *SpyEngine) Initialize(ctx context.Context) error {
	return s.Inner.Initialize(ctx)
}

func (s *SpyEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	return s.Inner.Execute(ctx, req)
}

func (s *SpyEngine) Shutdown(ctx context.Context) error {
	s.ShutdownCount.Add(1)
	if s.ShutdownErr != nil {
		return s.ShutdownErr
	}
	return s.Inner.Shutdown(ctx)
}

func (s *SpyEngine) WasCalled() bool {
	return s.ShutdownCount.Load() > 0
}

func (s *SpyEngine) CallCount() int {
	return int(s.ShutdownCount.Load())
}

// ---------------------------------------------------------------------------
// MockEngine.Shutdown contract
// ---------------------------------------------------------------------------

func TestMockEngine_Shutdown_ReturnsNilError(t *testing.T) {
	engine := NewMockEngine("test-model")
	err := engine.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestMockEngine_Shutdown_Idempotent(t *testing.T) {
	engine := NewMockEngine("test-model")

	for i := 0; i < 5; i++ {
		err := engine.Shutdown(context.Background())
		assert.NoError(t, err, "Shutdown call %d should not error", i+1)
	}
}

func TestMockEngine_Shutdown_AfterExecute(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx := context.Background()

	_, err := engine.Execute(ctx, &ExecutionRequest{
		Message: "hello",
	})
	require.NoError(t, err)

	err = engine.Shutdown(ctx)
	assert.NoError(t, err, "Shutdown after Execute should succeed")
}

func TestMockEngine_Shutdown_WithCancelledContext(t *testing.T) {
	engine := NewMockEngine("test-model")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := engine.Shutdown(ctx)
	assert.NoError(t, err, "MockEngine.Shutdown should succeed even with canceled context")
}

// ---------------------------------------------------------------------------
// SpyEngine — verifies tracking works correctly
// ---------------------------------------------------------------------------

func TestSpyEngine_TracksShutdownCalls(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	assert.False(t, spy.WasCalled(), "Shutdown should not be called yet")
	assert.Equal(t, 0, spy.CallCount())

	err := spy.Shutdown(context.Background())
	require.NoError(t, err)

	assert.True(t, spy.WasCalled(), "Shutdown should have been called")
	assert.Equal(t, 1, spy.CallCount())
}

func TestSpyEngine_TracksMultipleShutdownCalls(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	for i := 0; i < 3; i++ {
		err := spy.Shutdown(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, 3, spy.CallCount())
}

func TestSpyEngine_PropagatesShutdownError(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)
	spy.ShutdownErr = errors.New("shutdown failed: connection refused")

	err := spy.Shutdown(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown failed")
	assert.True(t, spy.WasCalled(), "Shutdown should still be tracked even on error")
}

func TestSpyEngine_DelegatesExecute(t *testing.T) {
	inner := NewMockEngine("test-model")
	spy := NewSpyEngine(inner)

	resp, err := spy.Execute(context.Background(), &ExecutionRequest{
		Message: "hello",
	})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Contains(t, resp.FinalOutput, "Mock response")
}

// ---------------------------------------------------------------------------
// CopilotEngine.Shutdown contract (no SDK required)
// ---------------------------------------------------------------------------

func TestCopilotEngine_Shutdown_NoInit(t *testing.T) {
	// Shutdown on an engine that was never initialized should be safe.
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	err := engine.Shutdown(context.Background())
	assert.NoError(t, err, "Shutdown on uninitialized CopilotEngine should not error")
}

func TestCopilotEngine_Shutdown_Idempotent(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()

	for i := 0; i < 3; i++ {
		err := engine.Shutdown(context.Background())
		assert.NoError(t, err, "Shutdown call %d should not error", i+1)
	}
}

func TestCopilotEngine_Shutdown_CleansWorkspace(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()

	// Simulate a workspace existing (without running the full SDK)
	tmpDir := t.TempDir()
	engine.workspacesMu.Lock()
	engine.workspaces = append(engine.workspaces, tmpDir)
	engine.workspacesMu.Unlock()

	err := engine.Shutdown(context.Background())
	require.NoError(t, err)

	// After shutdown, workspace should be cleared
	engine.workspacesMu.Lock()
	defer engine.workspacesMu.Unlock()
	require.Empty(t, engine.workspaces)
}

func TestCopilotEngine_Shutdown_WithCancelledContext(t *testing.T) {
	engine := NewCopilotEngineBuilder("test-model", nil).Build()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := engine.Shutdown(ctx)
	assert.NoError(t, err, "CopilotEngine.Shutdown should handle canceled context gracefully")
}

// ---------------------------------------------------------------------------
// AgentEngine interface compliance — static check
// ---------------------------------------------------------------------------

var (
	_ AgentEngine = (*MockEngine)(nil)
	_ AgentEngine = (*CopilotEngine)(nil)
	_ AgentEngine = (*SpyEngine)(nil)
)
