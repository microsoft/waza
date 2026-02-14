package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/utils"
)

// CopilotEngine integrates with GitHub Copilot SDK
type CopilotEngine struct {
	modelID string

	// Mutex to protect concurrent access to workspace and client
	mu        sync.Mutex
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
			modelID: modelID,
		},
	}
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
// This method is now concurrency-safe through mutex protection
func (e *CopilotEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	// Lock for the entire execution to ensure workspace/client isolation
	e.mu.Lock()
	defer e.mu.Unlock()

	start := time.Now()

	// Clean up any previous workspace and create fresh one
	if e.workspace != "" {
		if err := os.RemoveAll(e.workspace); err != nil {
			// Log but don't fail - try to create new workspace anyway
			fmt.Fprintf(os.Stderr, "Warning: failed to remove old workspace %s: %v\n", e.workspace, err)
		}
	}

	tmpDir, err := os.MkdirTemp("", "waza-*")
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
		if err := e.client.Stop(); err != nil {
			// Log but don't fail on cleanup error
			fmt.Printf("warning: failed to stop client: %v\n", err)
		}
	}

	client := copilot.NewClient(&copilot.ClientOptions{
		Cwd:      e.workspace,
		LogLevel: "error",
	})

	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start copilot client: %w", err)
	}
	e.client = client

	// note, we're assuming you're _in_ the directory with the skill until we get our
	// workspacing story in order.
	cwd, err := os.Getwd()

	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	// Build skill directories list: start with CWD, then add any from request
	// Use map for O(n) duplicate detection
	seen := make(map[string]bool)
	seen[cwd] = true
	skillDirs := []string{cwd}
	
	// Add skill directories from request, avoiding duplicates
	for _, path := range req.SkillPaths {
		if !seen[path] {
			seen[path] = true
			skillDirs = append(skillDirs, path)
		}
	}

	// Log skill directories in verbose mode
	for _, dir := range skillDirs {
		slog.Debug("Adding skill directory", "path", dir)
	}

	// Create session with updated API
	session, err := e.client.CreateSession(ctx, &copilot.SessionConfig{
		Model: e.modelID,
		// these are the directory for the skill itself.
		SkillDirectories: skillDirs,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer func() {
		if err := session.Destroy(); err != nil {
			fmt.Printf("warning: failed to destroy session: %v\n", err)
		}
	}()

	eventsCollector := NewSessionEventsCollector()

	// Event handler with updated API
	unsubscribe := session.On(eventsCollector.On)
	defer unsubscribe()

	unsubscribe = session.On(utils.SessionToSlog)
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

	errorMsg := ""

	select {
	case <-eventsCollector.Done():
		// Completed normally
	case <-timeoutCtx.Done():
		errorMsg = fmt.Sprintf("execution timed out after %ds", req.TimeoutSec)
	}

	duration := time.Since(start)

	if errorMsg == "" {
		errorMsg = eventsCollector.ErrorMessage()
	}

	// Build response
	resp := &ExecutionResponse{
		FinalOutput:      joinStrings(eventsCollector.OutputParts()),
		Events:           eventsCollector.SessionEvents(),
		ModelID:          e.modelID,
		SkillInvocations: eventsCollector.SkillInvocations,
		DurationMs:       duration.Milliseconds(),
		ToolCalls:        eventsCollector.ToolCalls(),
		ErrorMsg:         errorMsg,
		Success:          errorMsg == "",
		WorkspaceDir:     e.workspace,
	}

	return resp, nil
}

// Shutdown cleans up resources
func (e *CopilotEngine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.client != nil {
		if err := e.client.Stop(); err != nil {
			// Log but continue cleanup
			fmt.Printf("warning: failed to stop client: %v\n", err)
		}
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
