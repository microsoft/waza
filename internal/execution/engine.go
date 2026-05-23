package execution

import (
	"context"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
)

// AgentEngine is the interface for executing test prompts
type AgentEngine interface {
	// Initialize sets up the engine
	Initialize(ctx context.Context) error

	// Execute runs a test with the given stimulus
	Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error)

	// Shutdown cleans up resources. It is safe to call multiple times;
	// subsequent calls after the first are no-ops. After Shutdown returns,
	// SessionUsage results include data from session termination events.
	Shutdown(ctx context.Context) error

	// SessionUsage returns the final usage stats for a session, including
	// data from session.shutdown events that fire during Shutdown().
	// Returns nil if no usage data is available for the given session.
	SessionUsage(sessionID string) *models.UsageStats
}

// WorkspaceKeeper is an optional interface that engines can implement to support
// preserving temp workspaces after execution (for debugging).
type WorkspaceKeeper interface {
	SetKeepWorkspace(keep bool)
}

// MessageMode controls how a prompt is submitted to an existing session.
type MessageMode string

const (
	// MessageModeEnqueue sends the prompt using the Copilot SDK's enqueue mode.
	MessageModeEnqueue MessageMode = "enqueue"
)

// ExecutionRequest represents a test execution request
type ExecutionRequest struct {
	ModelID      string
	Message      string
	Context      map[string]any
	Resources    []ResourceFile
	Instructions []InstructionFile
	Tools        []copilot.Tool

	MessageMode MessageMode
	Streaming   bool

	// EphemeralSession keeps one-off sessions out of engine shutdown tracking.
	// New ephemeral sessions are deleted at the end of Execute. Resumed
	// ephemeral sessions are only disconnected because the caller does not own
	// the original session data.
	EphemeralSession bool

	// SkipWorkspaceCapture avoids post-run workspace snapshots for callers that
	// only need model/tool output, such as prompt graders.
	SkipWorkspaceCapture bool

	SessionID    string
	WorkspaceDir string // Reuse an existing workspace directory (for follow-up prompts)
	SkillName    string

	// TaskName and TaskDescription carry test-case metadata so mock engines can
	// echo them, enabling output_contains expectations that reference task-level
	// concepts (e.g., "recursive") without a real model.
	TaskName        string
	TaskDescription string

	SourceDir  string   // used when looking for workspace items via relative path, like skills.
	SkillPaths []string // Directories to search for skills
	NoSkills   bool     // When true, skip all skill loading
	// SuppressSkillBody prevents full target skill content from being appended
	// while still allowing skill discovery and compact summaries.
	SuppressSkillBody bool

	Timeout time.Duration

	// MCPServers configures MCP servers for the session. Keys are server names,
	// values follow the copilot SDK MCPServerConfig format (type/command/args).
	MCPServers map[string]copilot.MCPServerConfig

	// PermissionHandler called when the copilot SDK wants to determine if a tool can be used.
	// Default: allows all tools.
	PermissionHandler copilot.PermissionHandlerFunc

	// CancelOnSkillInvocation, when true, causes the execution context to be
	// canceled as soon as a SkillInvoked event is received. This allows trigger
	// tests to terminate early once the skill invocation they care about has been
	// detected, avoiding unnecessary wait for the agent to finish its full turn.
	CancelOnSkillInvocation bool
}

// ResourceFile represents a file resource
type ResourceFile struct {
	Path    string
	Content []byte
}

// InstructionFile represents a file whose content should be applied as agent instructions.
type InstructionFile struct {
	Path    string
	Content []byte
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
	WorkspaceDir     string            // Path to workspace directory (for file grading)
	WorkspaceFiles   map[string][]byte // Post-execution workspace file contents captured before session disconnect
	SessionID        string            // Copilot session ID
	Usage            *models.UsageStats
}

// ExtractMessages gets all assistant messages from events
func (r *ExecutionResponse) ExtractMessages() []string {
	var messages []string
	for _, evt := range r.Events {
		if evt.Type == copilot.SessionEventTypeAssistantMessage {
			if content, ok := copilotevents.Content(evt); ok {
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
