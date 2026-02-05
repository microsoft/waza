package execution

import (
	"context"
	"fmt"
	"time"
)

// MockEngine is a simple mock implementation for testing
type MockEngine struct {
	modelID string
}

// NewMockEngine creates a new mock engine
func NewMockEngine(modelID string) *MockEngine {
	return &MockEngine{
		modelID: modelID,
	}
}

func (m *MockEngine) Initialize(ctx context.Context) error {
	return nil
}

func (m *MockEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	start := time.Now()
	
	// Simple mock response
	output := fmt.Sprintf("Mock response for: %s", req.Message)
	
	// Add some context if files are present
	if len(req.Resources) > 0 {
		output += fmt.Sprintf("\nAnalyzed %d file(s)", len(req.Resources))
	}
	
	resp := &ExecutionResponse{
		FinalOutput:  output,
		Events:       []SessionEvent{},
		ModelID:      m.modelID,
		SkillInvoked: req.SkillName,
		DurationMs:   time.Since(start).Milliseconds(),
		ToolCalls:    []ToolCall{},
		Success:      true,
	}
	
	return resp, nil
}

func (m *MockEngine) Shutdown(ctx context.Context) error {
	return nil
}
