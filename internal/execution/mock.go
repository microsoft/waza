package execution

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// MockEngine is a simple mock implementation for testing
type MockEngine struct {
	modelID       string
	workspace     string
	gitResources  []GitResource
	keepWorkspace bool
	mtx           *sync.Mutex
	initCalled    atomic.Bool
}

// NewMockEngine creates a new mock engine
func NewMockEngine(modelID string) *MockEngine {
	return &MockEngine{
		modelID: modelID,
		mtx:     &sync.Mutex{},
	}
}

// SetKeepWorkspace enables or disables workspace preservation on shutdown.
func (m *MockEngine) SetKeepWorkspace(keep bool) {
	m.keepWorkspace = keep
}

func (m *MockEngine) Initialize(ctx context.Context) error {
	m.initCalled.Store(true)
	return nil
}

func (m *MockEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	if !m.initCalled.Load() {
		return nil, fmt.Errorf("engine was not initialized. Initialize needs to be called before Execute")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mtx.Lock()
	defer m.mtx.Unlock()

	start := time.Now()

	// Reuse workspace if provided (follow-up prompts), otherwise create fresh
	if req.WorkspaceDir != "" {
		m.workspace = req.WorkspaceDir
	} else {
		// Clean up any previous workspace before creating a new one
		if m.workspace != "" {
			for _, gr := range m.gitResources {
				if err := gr.Cleanup(ctx); err != nil {
					slog.Warn("failed to cleanup previous git resource", "error", err)
				}
			}
			m.gitResources = nil
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

		// Materialize git resources just like CopilotEngine does, so tests that
		// use the mock engine still exercise the worktree pipeline end-to-end.
		gitRes, err := CloneGitResources(ctx, req.GitResources, m.workspace)
		if err != nil {
			return nil, fmt.Errorf("failed to materialize mock git resources: %w", err)
		}
		m.gitResources = append(m.gitResources, gitRes...)
	}

	if _, err := ResolveWorkDir(m.workspace, req.WorkDir); err != nil {
		return nil, err
	}

	// Simple mock response
	output := fmt.Sprintf("Mock response for: %s", req.Message)

	// Echo task metadata so output_contains expectations that reference
	// task-level concepts (e.g., "recursive", "list") can match.
	if req.TaskName != "" {
		output += fmt.Sprintf("\nTask: %s", req.TaskName)
	}
	if req.TaskDescription != "" {
		output += fmt.Sprintf("\nDescription: %s", req.TaskDescription)
	}

	// Echo context metadata
	if len(req.Context) > 0 {
		output += "\nContext:"
		for k, v := range req.Context {
			output += fmt.Sprintf("\n  %s: %v", k, v)
		}
	}

	// Echo file paths and a content preview so output_contains expectations
	// against file content can match without needing a real model.
	if len(req.Resources) > 0 {
		output += fmt.Sprintf("\nAnalyzed %d file(s):", len(req.Resources))
		for _, r := range req.Resources {
			output += fmt.Sprintf("\n  - %s", r.Path)
			if len(r.Content) > 0 {
				preview := string(r.Content)
				if len(preview) > 1024 {
					preview = preview[:1024] + "...(truncated)"
				}
				output += "\n" + preview
			}
		}
	}

	// Pass through session ID for follow-up continuity
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("mock-session-%d", time.Now().UnixNano())
	}

	resp := &ExecutionResponse{
		FinalOutput:    output,
		Events:         []copilot.SessionEvent{},
		ModelID:        m.modelID,
		DurationMs:     time.Since(start).Milliseconds(),
		ToolCalls:      []models.ToolCall{},
		Success:        true,
		SessionID:      sessionID,
		WorkspaceDir:   m.workspace,
		WorkspaceFiles: captureWorkspaceFiles(m.workspace),
	}
	if req.SkipWorkspaceCapture {
		resp.WorkspaceFiles = nil
	}

	return resp, nil
}

func (m *MockEngine) Shutdown(ctx context.Context) error {
	for _, gr := range m.gitResources {
		if err := gr.Cleanup(ctx); err != nil {
			slog.Warn("failed to cleanup mock git resource", "error", err)
		}
	}
	m.gitResources = nil
	if m.workspace != "" {
		if m.keepWorkspace {
			fmt.Fprintf(os.Stderr, "Workspace preserved: %s\n", m.workspace)
		} else if err := os.RemoveAll(m.workspace); err != nil {
			return fmt.Errorf("failed to remove mock workspace %s: %w", m.workspace, err)
		}
		m.workspace = ""
	}
	return nil
}

func (m *MockEngine) SessionUsage(sessionID string) *models.UsageStats {
	return nil
}
