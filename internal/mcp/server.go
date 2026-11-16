package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/microsoft/waza/internal/webapi"
)

const protocolVersion = "2024-11-05"

// Server handles MCP protocol messages by delegating to existing JSON-RPC handlers.
type Server struct {
	hctx   *jsonrpc.HandlerContext
	reg    *jsonrpc.MethodRegistry
	logger *slog.Logger
}

// NewServer creates an MCP server backed by the given JSON-RPC handler context.
func NewServer(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	hctx := jsonrpc.NewHandlerContext()
	reg := jsonrpc.NewMethodRegistry()
	jsonrpc.RegisterHandlers(reg, hctx)
	return &Server{hctx: hctx, reg: reg, logger: logger}
}

// HandleRequest processes a single MCP JSON-RPC request and returns a response.
func (s *Server) HandleRequest(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		// Client acknowledgement — no response needed for notifications.
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	default:
		return &jsonrpc.Response{
			JSONRPC: "2.0",
			Error:   jsonrpc.ErrMethodNotFound(req.Method),
			ID:      req.ID,
		}
	}
}

// --- initialize ---

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
}

type capabilities struct {
	Tools *toolsCap `json:"tools,omitempty"`
}

type toolsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s *Server) handleInitialize(req *jsonrpc.Request) *jsonrpc.Response {
	return &jsonrpc.Response{
		JSONRPC: "2.0",
		Result: initializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    capabilities{Tools: &toolsCap{}},
			ServerInfo:      serverInfo{Name: "waza", Version: version()},
		},
		ID: req.ID,
	}
}

// --- tools/list ---

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

func (s *Server) handleToolsList(req *jsonrpc.Request) *jsonrpc.Response {
	return &jsonrpc.Response{
		JSONRPC: "2.0",
		Result:  toolsListResult{Tools: ToolsDef()},
		ID:      req.ID,
	}
}

// --- tools/call ---

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolsCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *Server) handleToolsCall(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	var p toolsCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return &jsonrpc.Response{
			JSONRPC: "2.0",
			Error:   jsonrpc.ErrInvalidParams(err.Error()),
			ID:      req.ID,
		}
	}

	usagetelemetry.RecordFeature(usagetelemetry.FeatureMCPServer)
	result, rpcErr := s.dispatchTool(ctx, p.Name, p.Arguments)
	if rpcErr != nil {
		return &jsonrpc.Response{
			JSONRPC: "2.0",
			Result: toolsCallResult{
				Content: []contentBlock{{Type: "text", Text: rpcErr.Error()}},
				IsError: true,
			},
			ID: req.ID,
		}
	}

	text, err := json.Marshal(result)
	if err != nil {
		return &jsonrpc.Response{
			JSONRPC: "2.0",
			Result: toolsCallResult{
				Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("marshal error: %v", err)}},
				IsError: true,
			},
			ID: req.ID,
		}
	}

	return &jsonrpc.Response{
		JSONRPC: "2.0",
		Result: toolsCallResult{
			Content: []contentBlock{{Type: "text", Text: string(text)}},
		},
		ID: req.ID,
	}
}

// dispatchTool maps MCP tool names to the underlying JSON-RPC handlers.
func (s *Server) dispatchTool(ctx context.Context, name string, args json.RawMessage) (any, *jsonrpc.Error) {
	switch name {
	case "waza_eval_list":
		return s.callHandler(ctx, "eval.list", args)
	case "waza_eval_get":
		return s.callHandler(ctx, "eval.get", args)
	case "waza_eval_validate":
		return s.callHandler(ctx, "eval.validate", args)
	case "waza_eval_run":
		return s.callHandler(ctx, "eval.run", args)
	case "waza_task_list":
		return s.callTaskList(ctx, args)
	case "waza_run_status":
		return s.callHandler(ctx, "run.status", args)
	case "waza_run_cancel":
		return s.callHandler(ctx, "run.cancel", args)
	case "waza_results_summary":
		return s.callResultsSummary(args)
	case "waza_results_runs":
		return s.callResultsRuns(args)
	case "waza_skill_check":
		return s.callSkillCheck(args)
	default:
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: fmt.Sprintf("unknown tool: %s", name)}
	}
}

// callHandler delegates to the existing JSON-RPC method handler.
func (s *Server) callHandler(ctx context.Context, method string, args json.RawMessage) (any, *jsonrpc.Error) {
	handler := s.reg.Lookup(method)
	if handler == nil {
		return nil, jsonrpc.ErrMethodNotFound(method)
	}
	if args == nil {
		args = json.RawMessage(`{}`)
	}
	return handler(ctx, args)
}

// callTaskList adapts waza_task_list (eval_path) → task.list (path).
func (s *Server) callTaskList(ctx context.Context, args json.RawMessage) (any, *jsonrpc.Error) {
	var p struct {
		EvalPath string `json:"eval_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}
	adapted, _ := json.Marshal(map[string]string{"path": p.EvalPath})
	return s.callHandler(ctx, "task.list", adapted)
}

// --- results handlers (from webapi store) ---

type dirParams struct {
	Dir string `json:"dir"`
}

func (s *Server) callResultsSummary(args json.RawMessage) (any, *jsonrpc.Error) {
	var p dirParams
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}
	dir := resolveDir(p.Dir)
	store := webapi.NewFileStore(dir)
	summary, err := store.Summary()
	if err != nil {
		return nil, jsonrpc.ErrInternalError(err.Error())
	}
	return summary, nil
}

func (s *Server) callResultsRuns(args json.RawMessage) (any, *jsonrpc.Error) {
	var p dirParams
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}
	dir := resolveDir(p.Dir)
	store := webapi.NewFileStore(dir)
	runs, err := store.ListRuns("timestamp", "desc")
	if err != nil {
		return nil, jsonrpc.ErrInternalError(err.Error())
	}
	return runs, nil
}

// --- skill check ---

type skillCheckParams struct {
	SkillPath string `json:"skill_path"`
}

type skillCheckResult struct {
	SkillPath   string   `json:"skillPath"`
	HasSkill    bool     `json:"hasSkill"`
	HasEval     bool     `json:"hasEval"`
	BrokenLinks int      `json:"brokenLinks"`
	LinkIssues  []string `json:"linkIssues,omitempty"`
	Message     string   `json:"message"`
}

func (s *Server) callSkillCheck(args json.RawMessage) (any, *jsonrpc.Error) {
	var p skillCheckParams
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}
	if p.SkillPath == "" {
		return nil, jsonrpc.ErrInvalidParams("skill_path is required")
	}

	absPath := p.SkillPath
	if !filepath.IsAbs(absPath) {
		wd, _ := os.Getwd()
		absPath = filepath.Join(wd, absPath)
	}

	result := skillCheckResult{SkillPath: absPath}

	// Check for SKILL.md
	skillFile := filepath.Join(absPath, "SKILL.md")
	if _, err := os.Stat(skillFile); err == nil {
		result.HasSkill = true
	}

	// Check for eval.yaml
	evalFile := filepath.Join(absPath, "eval.yaml")
	if _, err := os.Stat(evalFile); err == nil {
		result.HasEval = true
	} else {
		evalFile = filepath.Join(absPath, "eval.yml")
		if _, err := os.Stat(evalFile); err == nil {
			result.HasEval = true
		}
	}

	// Lightweight link check on SKILL.md
	if result.HasSkill {
		broken, issues := quickLinkCheck(absPath)
		result.BrokenLinks = broken
		result.LinkIssues = issues
	}

	switch {
	case result.HasSkill && result.HasEval:
		result.Message = "Skill has both SKILL.md and eval — ready for deeper check with waza check"
	case result.HasSkill:
		result.Message = "Skill has SKILL.md but no eval.yaml — consider adding an eval"
	case result.HasEval:
		result.Message = "Directory has eval.yaml but no SKILL.md"
	default:
		result.Message = "No SKILL.md or eval.yaml found in directory"
	}

	return &result, nil
}

// mdLinkPattern matches markdown link syntax [text](target).
var mdLinkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// quickLinkCheck does a fast regex-based scan of SKILL.md for broken local links.
func quickLinkCheck(skillDir string) (broken int, issues []string) {
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		return 0, nil
	}
	matches := mdLinkPattern.FindAllSubmatch(data, -1)
	for _, m := range matches {
		target := string(m[1])
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") ||
			strings.HasPrefix(target, "mdc:") || strings.HasPrefix(target, "#") {
			continue
		}
		if idx := strings.Index(target, "#"); idx >= 0 {
			target = target[:idx]
		}
		if target == "" {
			continue
		}
		resolved := filepath.Join(skillDir, filepath.FromSlash(target))
		if _, err := os.Stat(resolved); err != nil {
			broken++
			issues = append(issues, fmt.Sprintf("broken link: %s", target))
		}
	}
	return broken, issues
}

// resolveDir returns the given dir or falls back to CWD.
func resolveDir(dir string) string {
	if dir != "" {
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// version reads version.txt if available.
func version() string {
	// Try to find version.txt relative to the binary or CWD.
	for _, candidate := range []string{"version.txt", "../version.txt", "../../version.txt"} {
		data, err := os.ReadFile(candidate)
		if err == nil {
			v := string(data)
			if len(v) > 0 {
				// Trim newline
				for len(v) > 0 && (v[len(v)-1] == '\n' || v[len(v)-1] == '\r') {
					v = v[:len(v)-1]
				}
				return v
			}
		}
	}
	return "0.0.0-dev"
}
