package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// CodexEngine executes tasks through the local Codex CLI.
//
// The Codex CLI owns its own configuration and authentication discovery, so this
// engine intentionally does not parse ~/.codex/config.toml or auth.json. It
// invokes `codex exec` in Waza's isolated workspace and lets Codex load its
// normal config/auth state.
type CodexEngine struct {
	defaultModelID string
	binary         string
	binaryPath     string

	workspacesMu  sync.Mutex
	workspaces    []string
	keepWorkspace bool

	initCalled atomic.Bool
}

// CodexEngineOption configures a CodexEngine.
type CodexEngineOption func(*CodexEngine)

// WithCodexBinary overrides the Codex executable path. It is mainly useful for
// tests and for users who keep Codex outside PATH.
func WithCodexBinary(path string) CodexEngineOption {
	return func(e *CodexEngine) {
		if path != "" {
			e.binary = path
		}
	}
}

// NewCodexEngine creates a Codex-backed execution engine.
func NewCodexEngine(defaultModelID string, opts ...CodexEngineOption) *CodexEngine {
	e := &CodexEngine{
		defaultModelID: defaultModelID,
		binary:         "codex",
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// SetKeepWorkspace enables or disables workspace preservation on shutdown.
func (e *CodexEngine) SetKeepWorkspace(keep bool) {
	e.keepWorkspace = keep
}

// Initialize verifies that the Codex CLI can be found. Codex itself handles
// config/auth loading when the first task is executed.
func (e *CodexEngine) Initialize(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	path, err := exec.LookPath(e.binary)
	if err != nil {
		return fmt.Errorf("codex executable %q not found in PATH: %w", e.binary, err)
	}
	e.binaryPath = path
	e.initCalled.Store(true)
	return nil
}

// Execute runs a test prompt with `codex exec`.
func (e *CodexEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req was passed to CodexEngine.Execute")
	}
	if !e.initCalled.Load() {
		return nil, fmt.Errorf("engine was not initialized. Initialize needs to be called before Execute")
	}
	if req.Timeout <= 0 {
		return nil, fmt.Errorf("positive Timeout is required")
	}

	modelID := e.defaultModelID
	if req.ModelID != "" {
		modelID = req.ModelID
	}

	sourceDir := req.SourceDir
	if sourceDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory: %w", err)
		}
		sourceDir = cwd
	}

	start := time.Now()

	workspaceDir := req.WorkspaceDir
	if workspaceDir == "" {
		tmpDir, err := os.MkdirTemp("", "waza-codex-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create codex workspace: %w", err)
		}
		workspaceDir = tmpDir
		e.trackWorkspace(workspaceDir)

		if err := setupWorkspaceResources(workspaceDir, req.Resources); err != nil {
			return nil, fmt.Errorf("failed to setup codex workspace resources: %w", err)
		}
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	outputFile, err := os.CreateTemp("", "waza-codex-output-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create codex output file: %w", err)
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()
	defer os.Remove(outputPath) //nolint:errcheck

	if req.CancelOnSkillInvocation {
		return nil, fmt.Errorf("codex engine does not support skill invocation telemetry required by trigger tests")
	}

	args := e.buildArgs(req, modelID, workspaceDir, outputPath)

	prompt := e.buildPrompt(sourceDir, req)
	cmd := exec.CommandContext(ctx, e.binaryPath, args...)
	cmd.Dir = workspaceDir
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	telemetry := parseCodexJSONEvents(stdout.String())
	finalOutput := readCodexOutput(outputPath, telemetry.FinalOutput())

	errMsg := ""
	success := true
	if runErr != nil {
		success = false
		errMsg = strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = runErr.Error()
		} else {
			errMsg = fmt.Sprintf("%s: %v", errMsg, runErr)
		}
	}

	sessionID := telemetry.SessionID
	if sessionID == "" {
		sessionID = req.SessionID
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("codex-session-%d", time.Now().UnixNano())
	}

	return &ExecutionResponse{
		FinalOutput:    finalOutput,
		Events:         telemetry.Events,
		ModelID:        modelID,
		DurationMs:     time.Since(start).Milliseconds(),
		ToolCalls:      models.FilterToolCalls(telemetry.Events),
		ErrorMsg:       errMsg,
		Success:        success,
		WorkspaceDir:   workspaceDir,
		WorkspaceFiles: captureWorkspaceFiles(workspaceDir),
		SessionID:      sessionID,
		Usage:          telemetry.Usage,
	}, nil
}

func (e *CodexEngine) buildArgs(req *ExecutionRequest, modelID, workspaceDir, outputPath string) []string {
	common := []string{
		"-c", `approval_policy="never"`,
		"--skip-git-repo-check",
		"--output-last-message", outputPath,
	}
	if modelID != "" {
		common = append(common, "--model", modelID)
	}
	if req.ModelReasoningEffort != "" {
		common = append(common, "-c", fmt.Sprintf("model_reasoning_effort=%q", req.ModelReasoningEffort))
	}

	if req.SessionID != "" {
		args := []string{
			"exec",
			"resume",
			"--json",
			"-c", `sandbox_mode="workspace-write"`,
		}
		args = append(args, common...)
		args = append(args, req.SessionID, "-")
		return args
	}

	args := []string{
		"exec",
		"--json",
		"--cd", workspaceDir,
		"--sandbox", "workspace-write",
		"--color", "never",
	}
	args = append(args, common...)
	args = append(args, "-")
	return args
}

// Shutdown removes Codex workspaces created by this engine.
func (e *CodexEngine) Shutdown(ctx context.Context) error {
	workspaces := func() []string {
		e.workspacesMu.Lock()
		defer e.workspacesMu.Unlock()
		ws := e.workspaces
		e.workspaces = nil
		return ws
	}()

	for _, ws := range workspaces {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if ws == "" {
			continue
		}
		if e.keepWorkspace {
			fmt.Fprintf(os.Stderr, "Workspace preserved: %s\n", ws)
			continue
		}
		if err := os.RemoveAll(ws); err != nil {
			return fmt.Errorf("failed to remove codex workspace %s: %w", ws, err)
		}
	}
	return nil
}

// SessionUsage returns nil because codex exec does not currently expose Waza's
// Copilot-style session usage digest.
func (e *CodexEngine) SessionUsage(sessionID string) *models.UsageStats {
	return nil
}

func (e *CodexEngine) trackWorkspace(path string) {
	e.workspacesMu.Lock()
	defer e.workspacesMu.Unlock()
	e.workspaces = append(e.workspaces, path)
}

func (e *CodexEngine) buildPrompt(sourceDir string, req *ExecutionRequest) string {
	var sb strings.Builder

	if !req.NoSkills {
		skillDirs := skillDirsForRequest(sourceDir, req)
		if msg := buildSkillSystemMessage(skillDirs, req.SkillName); msg != "" {
			sb.WriteString(msg)
			sb.WriteString("\n")
		}
	}

	if req.TaskName != "" || req.TaskDescription != "" || len(req.Context) > 0 {
		sb.WriteString("<waza_task>\n")
		if req.TaskName != "" {
			fmt.Fprintf(&sb, "Name: %s\n", req.TaskName)
		}
		if req.TaskDescription != "" {
			fmt.Fprintf(&sb, "Description: %s\n", req.TaskDescription)
		}
		if len(req.Context) > 0 {
			sb.WriteString("Metadata:\n")
			for k, v := range req.Context {
				fmt.Fprintf(&sb, "- %s: %v\n", k, v)
			}
		}
		sb.WriteString("</waza_task>\n\n")
	}

	sb.WriteString(req.Message)
	return sb.String()
}

func readCodexOutput(outputPath, stdout string) string {
	data, err := os.ReadFile(outputPath)
	if err == nil && len(data) > 0 {
		return string(data)
	}
	return stdout
}

type codexTelemetry struct {
	SessionID string
	Events    []copilot.SessionEvent
	Usage     *models.UsageStats
}

func (t codexTelemetry) FinalOutput() string {
	for i := len(t.Events) - 1; i >= 0; i-- {
		evt := t.Events[i]
		if evt.Type == copilot.AssistantMessage && evt.Data.Content != nil {
			return *evt.Data.Content
		}
	}
	return ""
}

type codexJSONEvent struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"`
	Item     codexJSONItem  `json:"item"`
	Usage    codexJSONUsage `json:"usage"`
}

type codexJSONItem struct {
	ID               string                 `json:"id"`
	Type             string                 `json:"type"`
	Text             string                 `json:"text"`
	Name             string                 `json:"name"`
	ToolName         string                 `json:"tool_name"`
	Command          string                 `json:"command"`
	AggregatedOutput string                 `json:"aggregated_output"`
	Output           string                 `json:"output"`
	Status           string                 `json:"status"`
	ExitCode         *int                   `json:"exit_code"`
	Arguments        any                    `json:"arguments"`
	Changes          []codexJSONFileChange  `json:"changes"`
	Extra            map[string]interface{} `json:"-"`
}

type codexJSONFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type codexJSONUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

func parseCodexJSONEvents(stdout string) codexTelemetry {
	var telemetry codexTelemetry
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}

		var event codexJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		switch event.Type {
		case "thread.started":
			telemetry.SessionID = event.ThreadID
		case "item.started":
			if evt, ok := codexItemToSessionEvent(event.Item, false); ok {
				telemetry.Events = append(telemetry.Events, evt)
			}
		case "item.completed":
			if event.Item.Type == "agent_message" {
				if event.Item.Text != "" {
					text := event.Item.Text
					telemetry.Events = append(telemetry.Events, copilot.SessionEvent{
						Type: copilot.AssistantMessage,
						Data: copilot.Data{Content: &text},
					})
				}
				continue
			}
			if evt, ok := codexItemToSessionEvent(event.Item, true); ok {
				telemetry.Events = append(telemetry.Events, evt)
			}
		case "turn.completed":
			usage := &models.UsageStats{
				Turns:           1,
				InputTokens:     event.Usage.InputTokens,
				OutputTokens:    event.Usage.OutputTokens,
				CacheReadTokens: event.Usage.CachedInputTokens,
			}
			if !usage.IsZero() {
				telemetry.Usage = usage
			}
		}
	}
	return telemetry
}

func codexItemToSessionEvent(item codexJSONItem, completed bool) (copilot.SessionEvent, bool) {
	toolName, args, resultText, ok := codexToolFields(item)
	if !ok {
		return copilot.SessionEvent{}, false
	}

	toolCallID := item.ID
	if toolCallID == "" {
		toolCallID = fmt.Sprintf("%s-%s", item.Type, toolName)
	}

	if !completed {
		return copilot.SessionEvent{
			Type: copilot.ToolExecutionStart,
			Data: copilot.Data{
				ToolCallID: &toolCallID,
				ToolName:   &toolName,
				Arguments:  args,
			},
		}, true
	}

	success := item.Status != "failed"
	if item.ExitCode != nil && *item.ExitCode != 0 {
		success = false
	}
	return copilot.SessionEvent{
		Type: copilot.ToolExecutionComplete,
		Data: copilot.Data{
			ToolCallID: &toolCallID,
			ToolName:   &toolName,
			Success:    &success,
			Result: &copilot.Result{
				Content: &resultText,
			},
		},
	}, true
}

func codexToolFields(item codexJSONItem) (string, any, string, bool) {
	switch item.Type {
	case "command_execution":
		return "bash", map[string]any{"command": item.Command}, item.AggregatedOutput, true
	case "file_change":
		path := ""
		kind := ""
		if len(item.Changes) > 0 {
			path = item.Changes[0].Path
			kind = item.Changes[0].Kind
		}
		return "edit", map[string]any{"path": path, "command": kind}, item.Status, true
	}

	if strings.Contains(item.Type, "tool") {
		name := item.Name
		if name == "" {
			name = item.ToolName
		}
		if name == "" {
			name = item.Type
		}
		result := item.Output
		if result == "" {
			result = item.AggregatedOutput
		}
		if result == "" {
			result = item.Status
		}
		return name, item.Arguments, result, true
	}

	return "", nil, "", false
}

func skillDirsForRequest(cwd string, req *ExecutionRequest) []string {
	skillDirs := []string{cwd}
	seen := map[string]bool{cwd: true}

	for _, path := range req.SkillPaths {
		if !seen[path] {
			seen[path] = true
			skillDirs = append(skillDirs, path)
		}
	}

	return cleanSkillDirs(skillDirs)
}

func cleanSkillDirs(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		cleaned = append(cleaned, filepath.Clean(path))
	}
	return cleaned
}
