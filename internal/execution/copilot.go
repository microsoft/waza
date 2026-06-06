package execution

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/utils"
)

// CopilotEngine integrates with GitHub Copilot SDK
type CopilotEngine struct {
	defaultModelID string

	client CopilotClient

	// ownsClient is true when this engine constructed its own copilot client
	// and is therefore responsible for stopping it during Shutdown. When
	// false (the default — the engine is built on top of [SharedClient]),
	// Shutdown deletes only this engine's sessions and leaves the underlying
	// SDK process running for other engines / graders. The top-level
	// command must call [ShutdownSharedClient] to actually stop it.
	ownsClient bool

	startOnce sync.Once

	workspacesMu  sync.Mutex
	workspaces    []string      // workspaces to clean up at Shutdown
	gitResources  []GitResource // git resources to clean up before workspace removal
	keepWorkspace bool          // when true, skip workspace cleanup on shutdown

	// sessions maps session IDs to copilotSessions
	sessions   map[string]CopilotSession
	sessionsMu sync.Mutex

	// collectors tracks usage collectors by session ID so we can read
	// shutdown-event usage after client.Stop() fires session.shutdown events.
	usageCollectors   map[string]*SessionUsageCollector
	usageCollectorsMu sync.RWMutex

	shutdownOnce sync.Once
	shutdownErr  error

	provider customProviderConfig
}

type customProviderConfig struct {
	config *copilot.ProviderConfig
	host   string
	err    error
}

func (p customProviderConfig) enabled() bool {
	return p.config != nil && p.err == nil
}

func (p customProviderConfig) sessionConfig() *copilot.ProviderConfig {
	if p.config == nil {
		return nil
	}
	clone := *p.config
	return &clone
}

func (p customProviderConfig) applyToUsage(usage *models.UsageStats) {
	if usage == nil || !p.enabled() {
		return
	}
	usage.Provider = models.UsageProviderCustom
	usage.ProviderHost = p.host
}

// providerFromEnv assembles a BYOK ProviderConfig from environment variables.
// Returns an empty config when no custom provider base URL is set, leaving sessions on the
// default Copilot MaaS path. When set, fields are populated only when the
// matching env var is non-empty; the SDK supplies its own documented defaults
// for unset fields, so the executor does not hardcode provider-specific
// values.
//
// Each field accepts a short canonical name and a longer COPILOT_PROVIDER_*
// alias for backward compatibility. The short name wins when both are set.
//
//	COPILOT_BASE_URL      or COPILOT_PROVIDER_BASE_URL        - endpoint URL.
//	COPILOT_PROVIDER      or COPILOT_PROVIDER_TYPE            - provider type.
//	COPILOT_WIRE_API      or COPILOT_PROVIDER_WIRE_API        - wire format.
//	COPILOT_API_KEY       or COPILOT_PROVIDER_API_KEY         - API key.
//	COPILOT_BEARER_TOKEN  or COPILOT_PROVIDER_BEARER_TOKEN    - bearer token.
func providerFromEnv() customProviderConfig {
	base := envFirst("COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL")
	if base == "" {
		return customProviderConfig{}
	}
	host, err := providerHost(base)
	if err != nil {
		return customProviderConfig{err: err}
	}
	p := &copilot.ProviderConfig{BaseURL: base}
	if v := envFirst("COPILOT_PROVIDER", "COPILOT_PROVIDER_TYPE"); v != "" {
		p.Type = v
	}
	if v := envFirst("COPILOT_WIRE_API", "COPILOT_PROVIDER_WIRE_API"); v != "" {
		p.WireAPI = v
	}
	if v := envFirst("COPILOT_API_KEY", "COPILOT_PROVIDER_API_KEY"); v != "" {
		p.APIKey = v
	}
	if v := envFirst("COPILOT_BEARER_TOKEN", "COPILOT_PROVIDER_BEARER_TOKEN"); v != "" {
		p.BearerToken = v
	}
	return customProviderConfig{
		config: p,
		host:   host,
	}
}

func providerHost(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid custom Copilot provider base URL: could not parse URL")
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid custom Copilot provider base URL: must include scheme and host")
	}
	return u.Host, nil
}

// envFirst returns the value of the first non-empty environment variable
// among the provided names, or "" when none are set.
func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
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
//
// When `options.NewCopilotClient` is nil (the production path), the engine is
// wired to the process-wide shared client returned by [SharedClient]. The
// caller MUST invoke [ShutdownSharedClient] once at the top level after every
// engine has been Shutdown — engines built on top of the shared client will
// not stop it themselves. See docs/design/135-improve-concurrency.md (R2).
//
// When a `NewCopilotClient` factory is provided (test path), the engine
// constructs its own client and stops it during Shutdown as before.
func NewCopilotEngineBuilder(defaultModelID string, options *CopilotEngineBuilderOptions) *CopilotEngineBuilder {
	var client CopilotClient
	ownsClient := false
	provider := providerFromEnv()
	cliArgs := modelCLIArgs(defaultModelID, provider.enabled())

	if options == nil || options.NewCopilotClient == nil {
		// Production: share one SDK process across all engines + graders.
		client = SharedClient(SharedClientOptions{CLIArgs: cliArgs})
	} else {
		copilotOptions := &copilot.ClientOptions{
			// workspace is set at the session level, instead of at the client.
			LogLevel: "error",

			// SDK v1.0.0 moved CLIArgs onto the Connection. AutoStart/AutoRestart
			// are no longer configurable — the SDK starts on demand and restarts
			// internally. We still call client.Start() explicitly in Initialize().
			Connection: copilot.StdioConnection{Args: cliArgs},
		}
		client = options.NewCopilotClient(copilotOptions)
		ownsClient = true
	}

	builder := &CopilotEngineBuilder{
		engine: &CopilotEngine{
			defaultModelID: defaultModelID,
			ownsClient:     ownsClient,
			provider:       provider,
		},
	}

	builder.engine.client = client
	return builder
}

// modelCLIArgs returns the startup CLI args needed to pin the embedded
// Copilot CLI to defaultModelID, or an empty slice when no pinning should
// occur.
//
// When customProvider is true (a BYOK provider is configured via
// COPILOT_BASE_URL / COPILOT_PROVIDER_BASE_URL), we deliberately omit
// --model: the Copilot CLI validates the startup --model against the
// GitHub Copilot catalog before per-session ProviderConfig is applied, so
// provider-only model IDs (e.g. minimax-m2.7) would fail startup with
// `Model "..." is not available`. The per-session SessionConfig.Model +
// SessionConfig.Provider passed in Execute still selects the right model
// against the custom provider. See #305.
func modelCLIArgs(defaultModelID string, customProvider bool) []string {
	if defaultModelID == "" || customProvider {
		return []string{}
	}
	// --model forces the configured model at CLI startup, overriding any
	// user preference in the local Copilot settings.json (located under
	// the user's home directory on Unix and %USERPROFILE% on Windows) or
	// experiment flights that would otherwise cause the embedded CLI to
	// use unintended models. SessionConfig.Model still takes precedence
	// for per-session overrides via Execute.
	return []string{"--model", defaultModelID}
}

func (b *CopilotEngineBuilder) Build() *CopilotEngine {
	return b.engine
}

// CopilotClient returns the underlying Copilot SDK client backing this
// engine. Graders that need to spin up their own grading session (e.g.
// prompt graders) can use this to avoid spawning a fresh SDK process per
// invocation. Callers must NOT call Stop() on the returned client.
func (e *CopilotEngine) CopilotClient() CopilotClient {
	return e.client
}

// SetKeepWorkspace enables or disables workspace preservation on shutdown.
func (e *CopilotEngine) SetKeepWorkspace(keep bool) {
	e.keepWorkspace = keep
}

// Initialize sets up the Copilot client
func (e *CopilotEngine) Initialize(ctx context.Context) error {
	var startErr error

	e.startOnce.Do(func() {
		if e.provider.err != nil {
			startErr = e.provider.err
			return
		}

		// NOTE: we _have_ to use context.Background() - the copilot SDK is using exec.CommandContext() to run
		// the background process which means we _cannot_ cancel the context passed to this function or else
		// it'll kill the copilot process.
		// Tracking here: https://github.com/github/copilot-sdk/issues/668
		startErr = e.client.Start(context.Background())

		if startErr != nil {
			return
		}

		// BYOK mode: a custom provider redirects model traffic away from
		// GitHub Copilot MaaS, so the Copilot auth check is irrelevant.
		if e.provider.enabled() {
			return
		}

		authStatusResp, err := e.client.GetAuthStatus(ctx)

		if err != nil {
			if e.ownsClient {
				_ = e.client.Stop()
			}

			startErr = fmt.Errorf("failed to get copilot authentication status. Use any installed instance of copilot CLI and run \"copilot login\" before using this command: %w", err)
			return
		}

		if !authStatusResp.IsAuthenticated {
			if e.ownsClient {
				_ = e.client.Stop()
			}

			startErr = fmt.Errorf("copilot is not authenticated. Use any installed instance of copilot CLI and run \"copilot login\" before using this command")
			return
		}
	})

	if startErr != nil {
		return fmt.Errorf("copilot failed to start: %w", startErr)
	}

	return nil
}

// ListModels returns the available models from the Copilot backend.
func (e *CopilotEngine) ListModels(ctx context.Context) ([]copilot.ModelInfo, error) {
	return e.client.ListModels(ctx)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	start := time.Now()

	// Reuse an existing workspace when WorkspaceDir is provided (follow-up prompts),
	// otherwise create a fresh one from the request resources.
	var workspaceDir string
	if req.WorkspaceDir != "" {
		workspaceDir = req.WorkspaceDir
	} else {
		workspaceDir, err = e.setupWorkspace(ctx, req.Resources, req.GitResources)
		if err != nil {
			return nil, err
		}
	}

	workingDir, err := ResolveWorkDir(workspaceDir, req.WorkDir)
	if err != nil {
		return nil, err
	}

	// Build skill directories list and system message, unless skills are disabled
	var skillDirs []string
	var systemMessage *copilot.SystemMessageConfig
	var systemMessageParts []string
	if !req.NoSkills {
		skillDirs = e.getSkillDirs(sourceDir, req)
		if msg := buildSkillSystemMessage(skillDirs, req.SkillName, !req.SuppressSkillBody); msg != "" {
			systemMessageParts = append(systemMessageParts, msg)
		}
	}
	if msg := buildInstructionSystemMessage(req.Instructions); msg != "" {
		systemMessageParts = append(systemMessageParts, msg)
	}
	if len(systemMessageParts) > 0 {
		systemMessage = &copilot.SystemMessageConfig{
			Mode:    "append",
			Content: strings.Join(systemMessageParts, "\n"),
		}
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
			Tools: req.Tools,

			OnPermissionRequest: permRequestCallback,

			SkillDirectories: skillDirs,
			WorkingDirectory: workingDir,
			SystemMessage:    systemMessage,
			Streaming:        streamingPtr(req.Streaming),
			MCPServers:       req.MCPServers,
			Provider:         e.provider.sessionConfig(),
		})

		if err != nil {
			return nil, fmt.Errorf("failed to create session: %w", err)
		}
	} else {
		session, err = e.client.ResumeSessionWithOptions(ctx, req.SessionID, &copilot.ResumeSessionConfig{
			Model: modelID,
			Tools: req.Tools,

			OnPermissionRequest: permRequestCallback,

			// these are the directory for the skill itself.
			SkillDirectories: skillDirs,
			WorkingDirectory: workingDir,
			SystemMessage:    systemMessage,
			Streaming:        streamingPtr(req.Streaming),
			MCPServers:       req.MCPServers,
			Provider:         e.provider.sessionConfig(),
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
		if req.EphemeralSession && req.SessionID == "" {
			deleteCtx, cancelDelete := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancelDelete()
			if err := e.client.DeleteSession(deleteCtx, sessionID); err != nil {
				slog.Debug("failed to delete ephemeral session", "sessionID", sessionID, "error", err)
			}
		}
	}()

	eventsCollector := NewSessionEventsCollector()
	usageCollector := NewSessionUsageCollector()

	// When CancelOnSkillInvocation is set, derive a cancellable context so we
	// can abort SendAndWait as soon as a skill invocation event arrives. This
	// lets trigger tests terminate early once the skill fires, rather than
	// waiting for the agent to finish its full turn.
	canceledForSkill := false
	if req.CancelOnSkillInvocation {
		var cancelSkill context.CancelFunc
		ctx, cancelSkill = context.WithCancel(ctx)
		eventsCollector.SetOnSkillInvoked(func(_ SkillInvocation) {
			canceledForSkill = true
			cancelSkill()
		})
		defer cancelSkill() // no-op if already called, ensures cleanup
	}

	// Event handler — NOT deferred for unsubscribe because we need to receive
	// session.shutdown events later during client.Stop(). The usage handler is
	// stored in e.collectors so we can read final usage after shutdown.
	session.On(eventsCollector.On)
	session.On(usageCollector.On)

	if !req.EphemeralSession {
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
	}

	unsubscribe := session.On(utils.NewSessionToSlog())
	defer unsubscribe()

	// Send prompt with updated API
	_, err = session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: req.Message,
		Mode:   string(req.MessageMode),
	})

	var errMsg string

	if err != nil {
		// If the context was canceled because we detected a skill invocation
		// (CancelOnSkillInvocation), that's not an error — it's expected early
		// termination. We clear the error so the response reports success.
		if canceledForSkill && ctx.Err() == context.Canceled {
			err = nil
		} else {
			// errors that are returned inline, as part of the conversation, also come back
			// in the returned error. Rather than having one of those fun functions that returns
			// both an error and a result, I'll just put the error message in the ExecutionResponse.
			errMsg = err.Error()
		}
	}

	duration := time.Since(start)

	// Capture workspace files while they are still in post-execution state.
	// The deferred session.Disconnect() may modify or restore workspace files,
	// so we snapshot them now to guarantee graders see the agent's changes.
	var workspaceFiles map[string][]byte
	if !req.SkipWorkspaceCapture {
		workspaceFiles = captureWorkspaceFiles(workspaceDir)
	}

	// Build response
	usage := usageCollector.UsageStats()
	e.provider.applyToUsage(usage)
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
		Usage:            usage,
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

	// Only stop the underlying SDK client when this engine owns it.
	// Engines built on the process-wide shared client (the production path)
	// must leave the client running so other engines / graders can use it;
	// the top-level command stops it via [ShutdownSharedClient]. See
	// docs/design/135-improve-concurrency.md (R2).
	if e.ownsClient {
		if err := e.client.Stop(); err != nil {
			return fmt.Errorf("failed to stop client: %w", err)
		}
	}

	// remove the workspace folders - should be safe now that all the copilot sessions are shut down
	// and the tests are complete.
	workspaces, gitResources := func() ([]string, []GitResource) {
		e.workspacesMu.Lock()
		defer e.workspacesMu.Unlock()
		workspaces := e.workspaces
		gitRes := e.gitResources
		e.workspaces = nil
		e.gitResources = nil
		return workspaces, gitRes
	}()

	// Clean up git resources first — `git worktree remove` needs the
	// workspace directory to still exist so it can record the removal in
	// the source repo's .git/worktrees/ bookkeeping.
	for _, gr := range gitResources {
		if err := gr.Cleanup(ctx); err != nil {
			slog.Warn("failed to cleanup git resource", "error", err)
		}
	}

	for _, ws := range workspaces {
		if ws != "" {
			if e.keepWorkspace {
				fmt.Fprintf(os.Stderr, "Workspace preserved: %s\n", ws)
			} else if err := os.RemoveAll(ws); err != nil {
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
// When BYOK was active for this session, the returned stats include sanitized
// provider metadata so downstream consumers can label PremiumRequests accurately
// (custom endpoint vs Copilot MaaS).
func (e *CopilotEngine) SessionUsage(sessionID string) *models.UsageStats {
	e.usageCollectorsMu.RLock()
	defer e.usageCollectorsMu.RUnlock()

	var usage *models.UsageStats
	if u := e.usageCollectors[sessionID]; u != nil {
		usage = u.UsageStats()
		e.provider.applyToUsage(usage)
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

func (e *CopilotEngine) setupWorkspace(ctx context.Context, resources []ResourceFile, gitResources []models.GitResource) (string, error) {
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

	// Materialize git resources (e.g. worktrees) into the workspace.
	// These must be cleaned up BEFORE the workspace directory is removed
	// at shutdown so `git worktree remove` can update bookkeeping in the
	// source repository's .git/worktrees/.
	gitRes, err := CloneGitResources(ctx, gitResources, workspaceDir)
	if err != nil {
		return "", fmt.Errorf("failed to materialize git resources at workspace %s: %w", workspaceDir, err)
	}
	if len(gitRes) > 0 {
		e.workspacesMu.Lock()
		e.gitResources = append(e.gitResources, gitRes...)
		e.workspacesMu.Unlock()
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

func allowAllTools(request copilot.PermissionRequest, invocation copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
	return &rpc.PermissionDecisionApproveOnce{}, nil
}

// streamingPtr converts the caller's bool Streaming field into the *bool the
// SDK expects. We always pass an explicit value (never nil) so behavior is
// stable regardless of any future change to the SDK's default. If we ever
// need tri-state ("use SDK default") semantics, the caller field should
// become a *bool itself.
func streamingPtr(streaming bool) *bool {
	return copilot.Bool(streaming)
}

// skillDefinition holds the content extracted from a SKILL.md file.
type skillDefinition struct {
	Name        string
	Description string
	Content     string // full raw SKILL.md content
	Dir         string
}

// buildSkillSystemMessage scans skill directories for SKILL.md files and returns
// a system message that tells the agent about available skills. When
// injectSkillBody is true, the target skill (matching skillName) also gets its
// full definition injected. Other discovered skills always use compact summary
// entries only.
func buildSkillSystemMessage(skillDirs []string, skillName string, injectSkillBody bool) string {
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

	if injectSkillBody {
		// Inject full content for the target skill (first match only)
		for _, s := range skills {
			if skillName != "" && strings.EqualFold(s.Name, skillName) {
				sb.WriteString("\n<skill_context>\n")
				sb.WriteString(s.Content)
				sb.WriteString("\n</skill_context>\n")
				break
			}
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

func buildInstructionSystemMessage(instructions []InstructionFile) string {
	if len(instructions) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n<instruction_files>\n")
	sb.WriteString("The following instruction files apply to this evaluation task. Follow them as additional repository instructions.\n")
	for _, instruction := range instructions {
		if instruction.Path == "" {
			continue
		}
		sb.WriteString("\n<instruction_file>\n")
		fmt.Fprintf(&sb, "<path>%s</path>\n", instruction.Path)
		sb.WriteString("<content>\n")
		sb.Write(instruction.Content)
		if len(instruction.Content) == 0 || instruction.Content[len(instruction.Content)-1] != '\n' {
			sb.WriteString("\n")
		}
		sb.WriteString("</content>\n")
		sb.WriteString("</instruction_file>\n")
	}
	sb.WriteString("</instruction_files>\n")

	return sb.String()
}

// loadSkillDefinition reads a SKILL.md or .agent.md file from dir and extracts
// the skill/agent name, description and full content. SKILL.md takes priority.
// Returns nil if no definition file exists or parsing fails.
func loadSkillDefinition(dir string) *skillDefinition {
	// Try SKILL.md first (existing behavior)
	skillPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err == nil {
		content := string(data)
		name, desc := parseSkillFrontmatter(content)
		if name == "" {
			name = filepath.Base(dir)
		}
		slog.Debug("Loaded skill definition", "name", name, "dir", dir)
		return &skillDefinition{Name: name, Description: desc, Content: content, Dir: dir}
	}

	// Try .agent.md files
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() && skill.IsAgentFile(entry.Name()) {
			agentPath := filepath.Join(dir, entry.Name())
			agentData, readErr := os.ReadFile(agentPath)
			if readErr != nil {
				continue
			}
			content := string(agentData)
			name, desc := parseSkillFrontmatter(content)
			if name == "" {
				name = strings.TrimSuffix(entry.Name(), ".agent.md")
			}
			slog.Debug("Loaded agent definition", "name", name, "dir", dir)
			return &skillDefinition{Name: name, Description: desc, Content: content, Dir: dir}
		}
	}
	return nil
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
