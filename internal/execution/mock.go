package execution

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// MockEngine is a simple mock implementation for testing
type MockEngine struct {
	modelID   string
	workspace string
	mtx       *sync.Mutex
}

// NewMockEngine creates a new mock engine
func NewMockEngine(modelID string) *MockEngine {
	return &MockEngine{
		modelID: modelID,
		mtx:     &sync.Mutex{},
	}
}

func (m *MockEngine) Initialize(ctx context.Context) error {
	return nil
}

func (m *MockEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	start := time.Now()

	// Clean up any previous workspace before creating a new one
	if m.workspace != "" {
		if err := os.RemoveAll(m.workspace); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove old mock workspace %s: %v\n", m.workspace, err)
		}
		m.workspace = ""
	}

	// Create a temp workspace so graders that inspect files (e.g. FileGrader) have
	// a directory to work with, mirroring CopilotEngine behavior.
	tmpDir, err := os.MkdirTemp("", "waza-mock-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create mock workspace: %w", err)
	}
	m.workspace = tmpDir

	// Write request resources into the workspace
	if err := setupWorkspaceResources(m.workspace, req.Resources); err != nil {
		return nil, fmt.Errorf("failed to setup mock workspace resources: %w", err)
	}

	// Simple mock response
	output := fmt.Sprintf("Mock response for: %s", req.Message)

	// Add some context if files are present
	if len(req.Resources) > 0 {
		output += fmt.Sprintf("\nAnalyzed %d file(s)", len(req.Resources))
	}

	resp := &ExecutionResponse{
		FinalOutput:  output,
		Events:       []copilot.SessionEvent{},
		ModelID:      m.modelID,
		DurationMs:   time.Since(start).Milliseconds(),
		ToolCalls:    []models.ToolCall{},
		Success:      true,
		WorkspaceDir: m.workspace,
	}

	return resp, nil
}

func (m *MockEngine) Shutdown(ctx context.Context) error {
	if m.workspace != "" {
		if err := os.RemoveAll(m.workspace); err != nil {
			return fmt.Errorf("failed to remove mock workspace %s: %w", m.workspace, err)
		}
		m.workspace = ""
	}
	return nil
}

func (m *MockEngine) SessionUsage(sessionID string) *models.UsageStats {
	return nil
}
