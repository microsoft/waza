package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// CopilotEngine integrates with GitHub Copilot SDK
type CopilotEngine struct {
	modelID       string
	skillPaths    []string
	serverConfigs map[string]any
	timeoutSec    int
	streaming     bool

	client    *copilot.Client
	workspace string
}

// CopilotEngineBuilder builds a CopilotEngine with options
type CopilotEngineBuilder struct {
	engine *CopilotEngine
}

// NewCopilotEngineBuilder creates a builder for CopilotEngine
func NewCopilotEngineBuilder(modelID string) *CopilotEngineBuilder {
	return &CopilotEngineBuilder{
		engine: &CopilotEngine{
			modelID:    modelID,
			timeoutSec: 300,
			streaming:  true,
		},
	}
}

func (b *CopilotEngineBuilder) WithSkillPaths(paths []string) *CopilotEngineBuilder {
	b.engine.skillPaths = paths
	return b
}

func (b *CopilotEngineBuilder) WithServerConfigs(configs map[string]any) *CopilotEngineBuilder {
	b.engine.serverConfigs = configs
	return b
}

func (b *CopilotEngineBuilder) WithTimeout(seconds int) *CopilotEngineBuilder {
	b.engine.timeoutSec = seconds
	return b
}

func (b *CopilotEngineBuilder) WithStreaming(enabled bool) *CopilotEngineBuilder {
	b.engine.streaming = enabled
	return b
}

func (b *CopilotEngineBuilder) Build() *CopilotEngine {
	return b.engine
}

// Initialize sets up the Copilot client
// Note: workspace is created per-Execute call for test isolation
func (e *CopilotEngine) Initialize(ctx context.Context) error {
	// Client initialization is deferred to Execute() for better isolation
	// Each test execution gets a fresh workspace
	return nil
}

// Execute runs a test with Copilot SDK
func (e *CopilotEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	start := time.Now()

	// Clean up any previous workspace and create fresh one
	if e.workspace != "" {
		if err := os.RemoveAll(e.workspace); err != nil {
			// Log but don't fail - try to create new workspace anyway
			fmt.Fprintf(os.Stderr, "Warning: failed to remove old workspace %s: %v\n", e.workspace, err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "waza-go-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp workspace: %w", err)
	}
	e.workspace = tmpDir

	// Write resource files to workspace
	if err := e.setupResources(req.Resources); err != nil {
		return nil, fmt.Errorf("failed to setup resources: %w", err)
	}

	// Reinitialize client with new workspace
	if e.client != nil {
		e.client.Stop()
	}

	client := copilot.NewClient(&copilot.ClientOptions{
		Cwd:      e.workspace,
		LogLevel: "error",
	})

	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start copilot client: %w", err)
	}
	e.client = client

	// Create session with updated API
	session, err := e.client.CreateSession(ctx, &copilot.SessionConfig{
		Model: e.modelID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Destroy()

	// Collect events
	var events []SessionEvent
	var outputParts []string
	var errorMsg string
	done := make(chan struct{})

	// Event handler with updated API
	unsubscribe := session.On(func(evt copilot.SessionEvent) {
		// Convert to our event format
		event := SessionEvent{
			EventType: string(evt.Type),
			Timestamp: evt.Timestamp,
			Payload:   make(map[string]any),
		}

		// Extract message content from Data based on event type
		if evt.Type == "assistant.message" || evt.Type == "assistant.message_delta" {
			if evt.Data.Message != nil {
				event.Payload["content"] = *evt.Data.Message
				outputParts = append(outputParts, *evt.Data.Message)
			}
		}

		// Check for completion
		if evt.Type == "session.idle" {
			select {
			case <-done:
			default:
				close(done)
			}
		} else if evt.Type == "session.error" {
			if evt.Data.Message != nil {
				errorMsg = *evt.Data.Message
			}
			select {
			case <-done:
			default:
				close(done)
			}
		}

		events = append(events, event)
	})
	defer unsubscribe()

	// Send prompt with updated API
	_, err = session.Send(ctx, copilot.MessageOptions{
		Prompt: req.Message,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send prompt: %w", err)
	}

	// Wait for completion with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	defer cancel()

	select {
	case <-done:
		// Completed normally
	case <-timeoutCtx.Done():
		errorMsg = fmt.Sprintf("execution timed out after %ds", req.TimeoutSec)
	}

	duration := time.Since(start)

	// Build response
	resp := &ExecutionResponse{
		FinalOutput:  joinStrings(outputParts),
		Events:       events,
		ModelID:      e.modelID,
		SkillInvoked: req.SkillName,
		DurationMs:   duration.Milliseconds(),
		ToolCalls:    extractToolCalls(events),
		ErrorMsg:     errorMsg,
		Success:      errorMsg == "",
	}

	return resp, nil
}

// Shutdown cleans up resources
func (e *CopilotEngine) Shutdown(ctx context.Context) error {
	if e.client != nil {
		e.client.Stop()
		e.client = nil
	}

	if e.workspace != "" {
		if err := os.RemoveAll(e.workspace); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to remove workspace %s during shutdown: %v\n", e.workspace, err)
		}
		e.workspace = ""
	}

	return nil
}

// setupResources writes resource files to the workspace
func (e *CopilotEngine) setupResources(resources []ResourceFile) error {
	baseWorkspace := filepath.Clean(e.workspace)
	if baseWorkspace == "" {
		return fmt.Errorf("workspace is not set")
	}

	baseWithSep := baseWorkspace + string(os.PathSeparator)

	for _, res := range resources {
		if res.Path == "" {
			continue
		}

		relPath := filepath.Clean(res.Path)

		if filepath.IsAbs(relPath) {
			return fmt.Errorf("resource path %q must be relative", res.Path)
		}

		fullPath := filepath.Join(baseWorkspace, relPath)

		fullPathClean := filepath.Clean(fullPath)
		fullWithSep := fullPathClean + string(os.PathSeparator)

		if !strings.HasPrefix(fullWithSep, baseWithSep) {
			return fmt.Errorf("resource path %q escapes workspace", res.Path)
		}

		dir := filepath.Dir(fullPathClean)

		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		if err := os.WriteFile(fullPathClean, []byte(res.Content), 0644); err != nil {
			return err
		}
	}

	return nil
}

func joinStrings(parts []string) string {
	var builder strings.Builder
	for _, p := range parts {
		builder.WriteString(p)
	}
	return builder.String()
}

func extractToolCalls(events []SessionEvent) []ToolCall {
	var calls []ToolCall
	for _, evt := range events {
		if evt.EventType == "tool.execution_start" {
			call := ToolCall{
				Name:      getStringFromPayload(evt.Payload, "toolName"),
				Arguments: getMapFromPayload(evt.Payload, "arguments"),
			}
			calls = append(calls, call)
		}
	}
	return calls
}

func getStringFromPayload(payload map[string]any, key string) string {
	if val, ok := payload[key].(string); ok {
		return val
	}
	return ""
}

func getMapFromPayload(payload map[string]any, key string) map[string]any {
	if val, ok := payload[key].(map[string]any); ok {
		return val
	}
	return nil
}
