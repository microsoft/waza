package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// ClaudeCliEngine is an AgentEngine that drives the `claude` CLI (`claude -p`)
// as a subprocess, parsing its stream-json output into the common
// ExecutionResponse shape used by the rest of the evaluation pipeline.
type ClaudeCliEngine struct {
	modelID       string
	cliBinaryPath string
	sessionIDs    map[string]string // waza SessionID -> claude session_id
	sessionIDsMu  sync.Mutex

	workspacesMu  sync.Mutex
	workspaces    []string
	keepWorkspace bool

	// allowedTools is the list of tool names passed to --allowedTools. It
	// defaults to []string{"all"} when NewClaudeCliEngine is used. Callers
	// that need a tighter permission boundary can set this field after
	// construction but before Initialize. Use ["all"] to permit every tool
	// (matches the default CopilotEngine posture for eval workloads).
	allowedTools []string
}

var (
	_ AgentEngine     = (*ClaudeCliEngine)(nil)
	_ WorkspaceKeeper = (*ClaudeCliEngine)(nil)
)

// NewClaudeCliEngine resolves the claude CLI binary (CLAUDE_CLI_PATH overrides
// PATH lookup) and returns an engine ready to Execute prompts.
func NewClaudeCliEngine(modelID string) (*ClaudeCliEngine, error) {
	binaryPath := os.Getenv("CLAUDE_CLI_PATH")
	if binaryPath == "" {
		resolved, err := exec.LookPath("claude")
		if err != nil {
			return nil, fmt.Errorf("claude CLI not found in PATH; install Claude Code or set CLAUDE_CLI_PATH: %w", err)
		}
		binaryPath = resolved
	}

	return &ClaudeCliEngine{
		modelID:       modelID,
		cliBinaryPath: binaryPath,
		sessionIDs:    make(map[string]string),
		allowedTools:  []string{"all"},
	}, nil
}

// Initialize is a no-op; the CLI is launched per Execute call.
func (e *ClaudeCliEngine) Initialize(ctx context.Context) error {
	return nil
}

// Shutdown removes any temp workspaces this engine created (unless
// keepWorkspace is set). It is idempotent: a nil workspace slice yields nil.
func (e *ClaudeCliEngine) Shutdown(ctx context.Context) error {
	e.workspacesMu.Lock()
	defer e.workspacesMu.Unlock()

	if e.keepWorkspace {
		e.workspaces = nil
		return nil
	}

	var errs []error
	for _, dir := range e.workspaces {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, err)
		}
	}
	e.workspaces = nil
	return errors.Join(errs...)
}

// SessionUsage returns nil; the claude CLI stream does not expose token usage
// in a form mapped here.
func (e *ClaudeCliEngine) SessionUsage(sessionID string) *models.UsageStats {
	return nil
}

// SetKeepWorkspace toggles temp-workspace retention for debugging.
func (e *ClaudeCliEngine) SetKeepWorkspace(keep bool) {
	e.workspacesMu.Lock()
	defer e.workspacesMu.Unlock()
	e.keepWorkspace = keep
}

// Execute runs a single `claude -p` turn for the given request.
func (e *ClaudeCliEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	start := time.Now()

	// Reuse the caller-provided workspace (caller owns cleanup) or create a
	// temp one tracked for Shutdown.
	var workspaceDir string
	if req.WorkspaceDir != "" {
		workspaceDir = req.WorkspaceDir
	} else {
		dir, err := os.MkdirTemp("", "waza-claude-*")
		if err != nil {
			return nil, fmt.Errorf("creating workspace: %w", err)
		}
		workspaceDir = dir
		e.workspacesMu.Lock()
		e.workspaces = append(e.workspaces, dir)
		e.workspacesMu.Unlock()
	}

	if len(req.Resources) > 0 {
		if err := setupWorkspaceResources(workspaceDir, req.Resources); err != nil {
			return nil, fmt.Errorf("writing resources: %w", err)
		}
	}

	// Build the base args. --allowedTools is populated from e.allowedTools,
	// which defaults to "all" (matching CopilotEngine's permissive eval
	// posture) but can be narrowed to a safe subset per caller requirements.
	allowedTools := strings.Join(e.allowedTools, ",")
	args := []string{"-p", req.Message, "--output-format", "stream-json", "--verbose", "--allowedTools", allowedTools}

	// Engine-level model first, request override second (req wins, appended last).
	if e.modelID != "" {
		args = append(args, "--model", e.modelID)
	}
	if req.ModelID != "" {
		args = append(args, "--model", req.ModelID)
	}

	// Resume a prior claude session when we have a mapping for this waza session.
	if req.SessionID != "" {
		e.sessionIDsMu.Lock()
		if claudeID, ok := e.sessionIDs[req.SessionID]; ok && claudeID != "" {
			args = append(args, "--resume", claudeID)
		}
		e.sessionIDsMu.Unlock()
	}

	// Skill injection (unless disabled).
	if !req.NoSkills {
		if systemMsg := buildSkillSystemMessage(req.SkillPaths, req.SkillName, !req.SuppressSkillBody); systemMsg != "" {
			args = append(args, "--append-system-prompt", systemMsg)
		}
	}

	// Instruction injection.
	if msg := buildInstructionSystemMessage(req.Instructions); msg != "" {
		args = append(args, "--append-system-prompt", msg)
	}

	cmd := exec.CommandContext(ctx, e.cliBinaryPath, args...)
	cmd.Dir = workspaceDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opening claude CLI stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("opening claude CLI stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude CLI: %w", err)
	}

	// parseStream drains stderr concurrently while scanning stdout.
	result, parseErr := parseStream(stdout, stderr)
	waitErr := cmd.Wait()

	var workspaceFiles map[string][]byte
	if !req.SkipWorkspaceCapture {
		workspaceFiles = captureWorkspaceFiles(workspaceDir)
	}

	// Persist the claude session id so follow-up prompts can --resume it.
	if req.SessionID != "" && result.ClaudeSessionID != "" {
		e.sessionIDsMu.Lock()
		e.sessionIDs[req.SessionID] = result.ClaudeSessionID
		e.sessionIDsMu.Unlock()
	}

	success := result.Success && waitErr == nil && parseErr == nil

	resp := &ExecutionResponse{
		FinalOutput:    result.FinalOutput,
		Events:         []copilot.SessionEvent{},
		ModelID:        req.ModelID,
		ToolCalls:      result.ToolCalls,
		Success:        success,
		WorkspaceDir:   workspaceDir,
		WorkspaceFiles: workspaceFiles,
		SessionID:      req.SessionID,
		DurationMs:     time.Since(start).Milliseconds(),
	}

	if waitErr != nil {
		err := fmt.Errorf("claude CLI failed: %w; stderr: %s", waitErr, result.Stderr)
		resp.ErrorMsg = err.Error()
		return resp, err
	}
	if parseErr != nil {
		err := fmt.Errorf("parsing claude CLI stream: %w; stderr: %s", parseErr, result.Stderr)
		resp.ErrorMsg = err.Error()
		return resp, err
	}

	return resp, nil
}
