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
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"

	// auto-loads the embedded copilot CLI, over using the copilot CLI on the machine.
	_ "github.com/microsoft/waza/internal/embedded"
)

// CopilotEngine integrates with GitHub Copilot SDK
type CopilotEngine struct {
	defaultModelID string

	client CopilotClient

	startOnce sync.Once

	workspacesMu sync.Mutex
	workspaces   []string // workspaces to clean up at Shutdown

	// sessions maps session IDs to copilotSessions
	sessions   map[string]CopilotSession
	sessionsMu sync.Mutex

	// collectors tracks usage collectors by session ID so we can read
	// shutdown-event usage after client.Stop() fires session.shutdown events.
	usageCollectors   map[string]*SessionUsageCollector
	usageCollectorsMu sync.RWMutex

	shutdownOnce sync.Once
	shutdownErr  error
}

// CopilotEngineBuilder builds a CopilotEngine with options
type CopilotEngineBuilder struct {
	engine *CopilotEngine
}

type CopilotEngineBuilderOptions struct {
	NewCopilotClient func(clientOptions *copilot.ClientOptions) CopilotClient
}

// NewCopilotEngineBuilder creates a builder for CopilotEngine
//   - defaultModelID - used if no model ID is specified in session creation. Can be blank, which means the copilot
//     CLI will choose its own fallback model.
func NewCopilotEngineBuilder(defaultModelID string, options *CopilotEngineBuilderOptions) *CopilotEngineBuilder {
	var client CopilotClient

	copilotOptions := &copilot.ClientOptions{
		// workspace is set at the session level, instead of at the client.
		LogLevel: "error",

		AutoStart:   new(false), // we handle start in Initialize()
		AutoRestart: new(true),  // this is a default, but just in case the defaults change...
	}

	if options == nil || options.NewCopilotClient == nil {
		client = newCopilotClient(copilotOptions)
	} else {
		client = options.NewCopilotClient(copilotOptions)
	}

	builder := &CopilotEngineBuilder{
		engine: &CopilotEngine{
			defaultModelID: defaultModelID,
		},
	}

	builder.engine.client = client
	return builder
}

func (b *CopilotEngineBuilder) Build() *CopilotEngine {
	return b.engine
}

// Initialize sets up the Copilot client
func (e *CopilotEngine) Initialize(ctx context.Context) error {
	var startErr error

	e.startOnce.Do(func() {
		// NOTE: we _have_ to use context.Background() - the copilot SDK is using exec.CommandContext() to run
		// the background process which means we _cannot_ cancel the context passed to this function or else
		// it'll kill the copilot process.
		// Tracking here: https://github.com/github/copilot-sdk/issues/668
		startErr = e.client.Start(context.Background())

		if startErr != nil {
			return
		}

		authStatusResp, err := e.client.GetAuthStatus(ctx)

		if err != nil {
			_ = e.client.Stop()

			startErr = fmt.Errorf("failed to get copilot authentication status. Use any installed instance of copilot CLI and run \"copilot login\" before using this command: %w", err)
			return
		}

		if !authStatusResp.IsAuthenticated {
			_ = e.client.Stop()

			startErr = fmt.Errorf("copilot is not authenticated. Use any installed instance of copilot CLI and run \"copilot login\" before using this command")
			return
		}
	})

	if startErr != nil {
		return fmt.Errorf("copilot failed to start: %w", startErr)
	}

	return nil
}

// Execute runs a test with Copilot SDK
func (e *CopilotEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req was passed to CopilotEngine.Execute")
	}

	modelID, sourceDir, err := e.extractReqParams(req)

	if err != nil {
		return nil, err
	}

	start := time.Now()

	workspaceDir, err := e.setupWorkspace(req.Resources)

	if err != nil {
		return nil, err
	}

	// Build skill directories list: start with CWD, then add any from request
	skillDirs := e.getSkillDirs(sourceDir, req)

	// Load skill definitions from directories and build system message
	var systemMessage *copilot.SystemMessageConfig
	if msg := buildSkillSystemMessage(skillDirs, req.SkillName); msg != "" {
		systemMessage = &copilot.SystemMessageConfig{
			Mode:    "append",
			Content: msg,
		}
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var session CopilotSession

	permRequestCallback := allowAllTools

	if req.PermissionHandler != nil {
		permRequestCallback = req.PermissionHandler
	}

	if req.SessionID == "" {
		// Create session with updated API
		session, err = e.client.CreateSession(ctx, &copilot.SessionConfig{
			Model: modelID,

			OnPermissionRequest: permRequestCallback,

			SkillDirectories: skillDirs,
			WorkingDirectory: workspaceDir,
			SystemMessage:    systemMessage,
			MCPServers:       req.MCPServers,
		})

		if err != nil {
			return nil, fmt.Errorf("failed to create session: %w", err)
		}
	} else {
		session, err = e.client.ResumeSessionWithOptions(ctx, req.SessionID, &copilot.ResumeSessionConfig{
			Model: modelID,

			OnPermissionRequest: permRequestCallback,

			// these are the directory for the skill itself.
			SkillDirectories: skillDirs,
			WorkingDirectory: workspaceDir,
			SystemMessage:    systemMessage,
			MCPServers:       req.MCPServers,
		})

		if err != nil {
			return nil, fmt.Errorf("failed to resume session (%s): %w", req.SessionID, err)
		}
	}

	sessionID := session.SessionID()
	defer func() {
		// Close the session, release its resources, and trigger any session end events. The destroy
		// operation doesn't remove data and isn't final in that the caller can resume the session by
		// calling Execute again with [ExecutionRequest.SessionID] set
		if err := session.Disconnect(); err != nil {
			slog.Info("failed to destroy session", "sessionID", sessionID, "error", err)
		}
	}()

	eventsCollector := NewSessionEventsCollector()
	usageCollector := NewSessionUsageCollector()

	// Event handler — NOT deferred for unsubscribe because we need to receive
	// session.shutdown events later during client.Stop(). The usage handler is
	// stored in e.collectors so we can read final usage after shutdown.
	session.On(eventsCollector.On)
	session.On(usageCollector.On)

	e.sessionsMu.Lock()
	if e.sessions == nil {
		e.sessions = make(map[string]CopilotSession)
	}
	e.sessions[sessionID] = session
	e.sessionsMu.Unlock()

	e.usageCollectorsMu.Lock()
	if e.usageCollectors == nil {
		e.usageCollectors = make(map[string]*SessionUsageCollector)
	}
	e.usageCollectors[sessionID] = usageCollector
	e.usageCollectorsMu.Unlock()

	unsubscribe := session.On(utils.SessionToSlog)
	defer unsubscribe()

	// Send prompt with updated API
	_, err = session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: req.Message,
	})

	var errMsg string

	if err != nil {
		// errors that are returned inline, as part of the conversation, also come back
		// in the returned error. Rather than having one of those fun functions that returns
		// both an error and a result, I'll just put the error message in the ExecutionResponse.
		errMsg = err.Error()
	}

	duration := time.Since(start)

	// Capture workspace files while they are still in post-execution state.
	// The deferred session.Disconnect() may modify or restore workspace files,
	// so we snapshot them now to guarantee graders see the agent's changes.
	workspaceFiles := captureWorkspaceFiles(workspaceDir)

	// Build response
	resp := &ExecutionResponse{
		FinalOutput:      joinStrings(eventsCollector.OutputParts()),
		Events:           eventsCollector.SessionEvents(),
		ModelID:          modelID,
		SkillInvocations: eventsCollector.SkillInvocations,
		DurationMs:       duration.Milliseconds(),
		ToolCalls:        eventsCollector.ToolCalls(),
		ErrorMsg:         errMsg,
		Success:          err == nil,
		WorkspaceDir:     workspaceDir,
		WorkspaceFiles:   workspaceFiles,
		SessionID:        sessionID,
		Usage:            usageCollector.UsageStats(),
	}

	return resp, nil
}

// Shutdown cleans up resources, deleting session and workspace data. It is safe to call
// multiple times; subsequent calls after the first are no-ops that return the original
// error.
func (e *CopilotEngine) Shutdown(ctx context.Context) error {
	e.shutdownOnce.Do(func() {
		e.shutdownErr = e.doShutdown(ctx)
	})
	return e.shutdownErr
}

func (e *CopilotEngine) doShutdown(ctx context.Context) error {
	sessions := func() map[string]CopilotSession {
		e.sessionsMu.Lock()
		defer e.sessionsMu.Unlock()
		s := e.sessions
		e.sessions = nil
		return s
	}()

	for id := range sessions {
		if err := e.client.DeleteSession(ctx, id); err != nil {
			slog.Debug("failed to delete session", "sessionID", id, "error", err)
		}
	}

	if err := e.client.Stop(); err != nil {
		return fmt.Errorf("failed to stop client: %w", err)
	}

	// remove the workspace folders - should be safe now that all the copilot sessions are shut down
	// and the tests are complete.
	workspaces := func() []string {
		e.workspacesMu.Lock()
		defer e.workspacesMu.Unlock()
		workspaces := e.workspaces
		e.workspaces = nil
		return workspaces
	}()

	for _, ws := range workspaces {
		if ws != "" {
			if err := os.RemoveAll(ws); err != nil {
				// errors here probably indicate some issue with our code continuing to lock files
				// even after tests have completed...
				slog.Warn("failed to cleanup stale workspace", "path", ws, "error", err)
			}
		}
	}

	return nil
}

// SessionUsage returns the final usage stats for a session. Call after Shutdown()
// to get data from session.shutdown events (ModelMetrics, TotalPremiumRequests).
func (e *CopilotEngine) SessionUsage(sessionID string) *models.UsageStats {
	e.usageCollectorsMu.RLock()
	defer e.usageCollectorsMu.RUnlock()

	var usage *models.UsageStats
	if u := e.usageCollectors[sessionID]; u != nil {
		usage = u.UsageStats()
	}
	return usage
}

func (e *CopilotEngine) extractReqParams(req *ExecutionRequest) (modelID string, sourceDir string, err error) {
	modelID = e.defaultModelID

	if req.ModelID != "" {
		modelID = req.ModelID // override the default model for the engine
	}

	sourceDir = req.SourceDir

	if req.SourceDir == "" {
		cwd, err := os.Getwd()

		if err != nil {
			return "", "", fmt.Errorf("failed to get current directory: %w", err)
		}

		sourceDir = cwd
	}

	if req.Timeout <= 0 {
		return "", "", fmt.Errorf("positive Timeout is required")
	}

	return modelID, sourceDir, nil
}

func (*CopilotEngine) getSkillDirs(cwd string, req *ExecutionRequest) []string {
	skillDirs := []string{cwd}

	seen := map[string]bool{
		cwd: true,
	}

	// Add skill directories from request, avoiding duplicates
	for _, path := range req.SkillPaths {
		if !seen[path] {
			seen[path] = true
			skillDirs = append(skillDirs, path)
		} else {
			slog.Warn("Skill directory included more than once in request", "path", path)
		}
	}

	// Log skill directories in verbose mode
	for _, dir := range skillDirs {
		slog.Debug("Adding skill directory", "path", dir)
	}

	return skillDirs
}

func (e *CopilotEngine) setupWorkspace(resources []ResourceFile) (string, error) {
	workspaceDir, err := os.MkdirTemp("", "waza-*")

	if err != nil {
		return "", fmt.Errorf("failed to create temp workspace: %w", err)
	}

	e.workspacesMu.Lock()
	e.workspaces = append(e.workspaces, workspaceDir)
	e.workspacesMu.Unlock()

	// Write resource files to workspace
	if err := setupWorkspaceResources(workspaceDir, resources); err != nil {
		return "", fmt.Errorf("failed to setup resources at workspace %s: %w", workspaceDir, err)
	}

	return workspaceDir, nil
}

// captureWorkspaceFiles reads all files from the workspace directory into memory,
// preserving the post-execution state. This is called before session.Disconnect()
// to ensure graders see the agent's modifications even if the SDK restores the
// workspace to its pre-execution state during disconnect.
// Keys are forward-slash-separated paths relative to dir for cross-platform consistency.
func captureWorkspaceFiles(dir string) map[string][]byte {
	if dir == "" {
		return nil
	}

	files := make(map[string][]byte)
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		// Normalize to forward slashes so map keys match eval YAML paths on all platforms.
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	return files
}

func joinStrings(parts []string) string {
	var builder strings.Builder
	for _, p := range parts {
		builder.WriteString(p)
	}
	return builder.String()
}

func allowAllTools(request copilot.PermissionRequest, invocation copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	// value for 'Kind' came from the permissions_test.go in the Copilot SDK.
	return copilot.PermissionRequestResult{Kind: "approved"}, nil
}

// skillDefinition holds the content extracted from a SKILL.md file.
type skillDefinition struct {
	Name        string
	Description string
	Content     string // full raw SKILL.md content
	Dir         string
}

// buildSkillSystemMessage scans skill directories for SKILL.md files and returns
// a system message that tells the agent about available skills. For the target
// skill (matching skillName), the full SKILL.md content is injected. For other
// discovered skills, only a compact summary is included.
func buildSkillSystemMessage(skillDirs []string, skillName string) string {
	var skills []skillDefinition

	for _, dir := range skillDirs {
		// Check direct SKILL.md in this directory
		sd := loadSkillDefinition(dir)
		if sd != nil {
			skills = append(skills, *sd)
			continue
		}

		// Walk one level of subdirectories to find nested skills
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// Skip hidden dirs, node_modules, vendor
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				continue
			}
			if sd := loadSkillDefinition(filepath.Join(dir, name)); sd != nil {
				skills = append(skills, *sd)
			}
		}
	}

	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder

	// Inject full content for the target skill (first match only)
	for _, s := range skills {
		if skillName != "" && strings.EqualFold(s.Name, skillName) {
			sb.WriteString("\n<skill_context>\n")
			sb.WriteString(s.Content)
			sb.WriteString("\n</skill_context>\n")
			break
		}
	}

	// Summary block for all discovered skills
	sb.WriteString("\n<available_skills>\n")
	for _, s := range skills {
		sb.WriteString("<skill>\n")
		fmt.Fprintf(&sb, "  <name>%s</name>\n", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&sb, "  <description>%s</description>\n", s.Description)
		}
		sb.WriteString("</skill>\n")
	}
	sb.WriteString("</available_skills>\n")

	return sb.String()
}

// loadSkillDefinition reads a SKILL.md file from dir and extracts the skill
// name, description and full content. Returns nil if no SKILL.md exists or
// parsing fails.
func loadSkillDefinition(dir string) *skillDefinition {
	skillPath := filepath.Join(dir, "SKILL.md")

	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil
	}

	content := string(data)
	name, desc := parseSkillFrontmatter(content)
	if name == "" {
		// Fall back to directory name
		name = filepath.Base(dir)
	}

	slog.Debug("Loaded skill definition", "name", name, "dir", dir)
	return &skillDefinition{Name: name, Description: desc, Content: content, Dir: dir}
}

// parseSkillFrontmatter extracts name and description from SKILL.md YAML
// frontmatter. Avoids importing the skill package to keep execution decoupled.
func parseSkillFrontmatter(content string) (name, description string) {
	if !strings.HasPrefix(content, "---") {
		return "", ""
	}

	rest := content[3:]
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", ""
	}

	yamlBlock := rest[:idx]

	for _, line := range strings.Split(yamlBlock, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = strings.Trim(name, "\"'")
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			description = strings.Trim(description, "\"'")
		}
	}

	return name, description
}
