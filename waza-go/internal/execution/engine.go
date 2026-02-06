package execution

import (
	"context"
	"strings"
	"time"
)

// AgentEngine is the interface for executing test prompts
type AgentEngine interface {
	// Initialize sets up the engine
	Initialize(ctx context.Context) error

	// Execute runs a test with the given stimulus
	Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error)

	// Shutdown cleans up resources
	Shutdown(ctx context.Context) error
}

// ExecutionRequest represents a test execution request
type ExecutionRequest struct {
	TestID     string
	Message    string
	Context    map[string]any
	Resources  []ResourceFile
	SkillName  string
	TimeoutSec int
}

// ResourceFile represents a file resource
type ResourceFile struct {
	Path    string
	Content string
}

// ExecutionResponse represents the result of an execution
type ExecutionResponse struct {
	FinalOutput  string
	Events       []SessionEvent
	ModelID      string
	SkillInvoked string
	DurationMs   int64
	ToolCalls    []ToolCall
	ErrorMsg     string
	Success      bool
}

// SessionEvent represents an event during execution
type SessionEvent struct {
	EventType string
	Timestamp time.Time
	Payload   map[string]any
}

// ToolCall represents a tool invocation
type ToolCall struct {
	Name      string
	Arguments map[string]any
	Result    any
	Success   bool
}

// ExtractMessages gets all assistant messages from events
func (r *ExecutionResponse) ExtractMessages() []string {
	var messages []string
	for _, evt := range r.Events {
		if evt.EventType == "assistant.message" {
			if content, ok := evt.Payload["content"].(string); ok {
				messages = append(messages, content)
			}
		}
	}
	return messages
}

// ContainsText checks if output contains text (case-insensitive)
func (r *ExecutionResponse) ContainsText(text string) bool {
	// Simple implementation - could be made more sophisticated
	return contains(r.FinalOutput, text)
}

func contains(haystack, needle string) bool {
	// Case-insensitive substring search
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
