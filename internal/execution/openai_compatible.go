package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

const defaultOpenAICompatibleModel = "local-model"

// OpenAICompatibleEngine executes prompts against an OpenAI-compatible chat completions API.
type OpenAICompatibleEngine struct {
	endpoint string
	modelID  string
	apiKey   string
	client   *http.Client

	mu      sync.Mutex
	usage   map[string]*models.UsageStats
	counter int

	workspaces     []string
	keepWorkspace  bool
	shutdownCalled bool
}

func NewOpenAICompatibleEngine(endpoint, modelID, apiKey string) (*OpenAICompatibleEngine, error) {
	chatURL, err := normalizeOpenAICompatibleEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = defaultOpenAICompatibleModel
	}
	return &OpenAICompatibleEngine{
		endpoint: chatURL,
		modelID:  modelID,
		apiKey:   strings.TrimSpace(apiKey),
		client:   &http.Client{Timeout: 5 * time.Minute},
		usage:    make(map[string]*models.UsageStats),
	}, nil
}

func normalizeOpenAICompatibleEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("openai-compatible endpoint is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid openai-compatible endpoint %q: must include scheme and host", raw)
	}
	u.RawQuery = ""
	u.Fragment = ""
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		u.Path = path
	case strings.HasSuffix(path, "/v1"):
		u.Path = path + "/chat/completions"
	case path == "":
		u.Path = "/v1/chat/completions"
	default:
		u.Path = path + "/v1/chat/completions"
	}
	return u.String(), nil
}

func (e *OpenAICompatibleEngine) Initialize(context.Context) error {
	return nil
}

func (e *OpenAICompatibleEngine) SetKeepWorkspace(keep bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keepWorkspace = keep
}

func (e *OpenAICompatibleEngine) Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("nil req was passed to OpenAICompatibleEngine.Execute")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	sourceDir := req.SourceDir
	if sourceDir == "" {
		sourceDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory: %w", err)
		}
	}

	modelID := e.modelID
	if strings.TrimSpace(req.ModelID) != "" {
		modelID = strings.TrimSpace(req.ModelID)
	}
	start := time.Now()

	workspaceDir := req.WorkspaceDir
	if workspaceDir == "" {
		workspaceDir, err = os.MkdirTemp("", "waza-openai-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create openai-compatible workspace: %w", err)
		}
		if err := setupWorkspaceResources(workspaceDir, req.Resources); err != nil {
			_ = os.RemoveAll(workspaceDir)
			return nil, fmt.Errorf("failed to setup openai-compatible workspace resources: %w", err)
		}
		e.mu.Lock()
		e.workspaces = append(e.workspaces, workspaceDir)
		e.mu.Unlock()
	}
	if _, err := ResolveWorkDir(workspaceDir, req.WorkDir); err != nil {
		return nil, err
	}

	output, usage, errMsg, success, err := e.sendChatCompletion(ctx, modelID, req, sourceDir)
	if err != nil {
		if workspaceDir != req.WorkspaceDir {
			_ = os.RemoveAll(workspaceDir)
		}
		return nil, err
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = e.nextSessionID()
	}
	if usage != nil {
		e.mu.Lock()
		e.usage[sessionID] = usage
		e.mu.Unlock()
	}

	resp := &ExecutionResponse{
		FinalOutput:    output,
		Events:         []copilot.SessionEvent{},
		ModelID:        modelID,
		DurationMs:     time.Since(start).Milliseconds(),
		ToolCalls:      []models.ToolCall{},
		ErrorMsg:       errMsg,
		Success:        success,
		SessionID:      sessionID,
		WorkspaceDir:   workspaceDir,
		WorkspaceFiles: captureWorkspaceFiles(workspaceDir),
		Usage:          usage,
	}
	if req.SkipWorkspaceCapture {
		resp.WorkspaceFiles = nil
	}
	return resp, nil
}

func (e *OpenAICompatibleEngine) nextSessionID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counter++
	return fmt.Sprintf("openai-compatible-%d", e.counter)
}

func (e *OpenAICompatibleEngine) sendChatCompletion(ctx context.Context, modelID string, req *ExecutionRequest, sourceDir string) (string, *models.UsageStats, string, bool, error) {
	payload := openAICompatibleChatRequest{
		Model:    modelID,
		Messages: buildOpenAICompatibleMessages(req, sourceDir),
		Stream:   false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, "", false, fmt.Errorf("encoding openai-compatible request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, "", false, fmt.Errorf("creating openai-compatible request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := e.apiKey; apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	} else if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return "", nil, "", false, fmt.Errorf("calling openai-compatible endpoint: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, "", false, fmt.Errorf("reading openai-compatible response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, string(data), false, nil
	}

	var decoded openAICompatibleChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", nil, "", false, fmt.Errorf("decoding openai-compatible response: %w", err)
	}
	var output string
	if len(decoded.Choices) > 0 {
		output = decoded.Choices[0].Message.Content
	}
	usage := decoded.Usage.toUsageStats(modelID, e.endpoint)
	return output, usage, "", true, nil
}

func buildOpenAICompatibleMessages(req *ExecutionRequest, sourceDir string) []openAICompatibleMessage {
	var system []string
	if !req.NoSkills {
		skillDirs := skillDirsForRequest(sourceDir, req)
		if msg := buildSkillSystemMessage(skillDirs, req.SkillName, !req.SuppressSkillBody); msg != "" {
			system = append(system, msg)
		}
	}
	for _, f := range req.Instructions {
		if len(f.Content) > 0 {
			system = append(system, fmt.Sprintf("# %s\n%s", f.Path, string(f.Content)))
		}
	}
	if len(req.Context) > 0 {
		if b, err := json.MarshalIndent(req.Context, "", "  "); err == nil {
			system = append(system, "Context:\n"+string(b))
		}
	}
	if len(req.Resources) > 0 {
		var b strings.Builder
		b.WriteString("Files available to the task:\n")
		for _, r := range req.Resources {
			fmt.Fprintf(&b, "\n## %s\n%s\n", r.Path, string(r.Content))
		}
		system = append(system, b.String())
	}

	messages := make([]openAICompatibleMessage, 0, 2)
	if len(system) > 0 {
		messages = append(messages, openAICompatibleMessage{
			Role:    "system",
			Content: strings.Join(system, "\n\n"),
		})
	}
	messages = append(messages, openAICompatibleMessage{
		Role:    "user",
		Content: req.Message,
	})
	return messages
}

func (e *OpenAICompatibleEngine) Shutdown(context.Context) error {
	e.mu.Lock()
	if e.shutdownCalled {
		e.mu.Unlock()
		return nil
	}
	e.shutdownCalled = true
	keep := e.keepWorkspace
	workspaces := append([]string(nil), e.workspaces...)
	e.workspaces = nil
	e.mu.Unlock()

	if keep {
		for _, workspace := range workspaces {
			fmt.Fprintf(os.Stderr, "Workspace preserved: %s\n", workspace)
		}
		return nil
	}
	for _, workspace := range workspaces {
		if err := os.RemoveAll(workspace); err != nil {
			return fmt.Errorf("failed to remove openai-compatible workspace %s: %w", workspace, err)
		}
	}
	return nil
}

func (e *OpenAICompatibleEngine) SessionUsage(sessionID string) *models.UsageStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usage[sessionID]
}

type openAICompatibleChatRequest struct {
	Model    string                    `json:"model"`
	Messages []openAICompatibleMessage `json:"messages"`
	Stream   bool                      `json:"stream"`
}

type openAICompatibleMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAICompatibleChatResponse struct {
	Choices []struct {
		Message openAICompatibleMessage `json:"message"`
	} `json:"choices"`
	Usage openAICompatibleUsage `json:"usage"`
}

type openAICompatibleUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u openAICompatibleUsage) toUsageStats(modelID, endpoint string) *models.UsageStats {
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	host := ""
	if parsed, err := url.Parse(endpoint); err == nil {
		host = parsed.Host
	}
	stats := &models.UsageStats{
		Turns:           1,
		InputTokens:     u.PromptTokens,
		OutputTokens:    u.CompletionTokens,
		PremiumRequests: 1,
		Provider:        models.UsageProviderCustom,
		ProviderHost:    host,
		ModelMetrics: map[string]models.ModelUsage{
			modelID: {
				InputTokens:  u.PromptTokens,
				OutputTokens: u.CompletionTokens,
				RequestCount: 1,
			},
		},
	}
	return stats
}
