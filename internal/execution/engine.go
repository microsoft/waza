package execution

import (
	"context"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/models"
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
	SkillPaths []string // Directories to search for skills
	TimeoutSec int
}

// ResourceFile represents a file resource
type ResourceFile struct {
	Path    string
	Content string
}

type SkillInvocation struct {
	// Name of the invoked skill
	Name string
	// Path of the invoked SKILL.md
	Path string
}

// ExecutionResponse represents the result of an execution
type ExecutionResponse struct {
	FinalOutput      string
	Events           []copilot.SessionEvent
	ModelID          string
	SkillInvocations []SkillInvocation
	DurationMs       int64
	ToolCalls        []models.ToolCall
	ErrorMsg         string
	Success          bool
	WorkspaceDir     string // Path to workspace directory (for file grading)
}

// ExtractMessages gets all assistant messages from events
func (r *ExecutionResponse) ExtractMessages() []string {
	var messages []string
	for _, evt := range r.Events {
		if evt.Type == copilot.AssistantMessage {
			if evt.Data.Content != nil {
				messages = append(messages, *evt.Data.Content)
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
