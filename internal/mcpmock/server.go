package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const protocolVersion = "2024-11-05"

type Server struct {
	cfg    *Config
	logger *slog.Logger
}

func NewServer(cfg *Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{cfg: cfg, logger: logger}
}

func (s *Server) HandleRequest(_ context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	switch req.Method {
	case "initialize":
		return &jsonrpc.Response{
			JSONRPC: "2.0",
			Result: map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]string{
					"name":    s.cfg.Name,
					"version": "0.0.0-mock",
				},
			},
			ID: req.ID,
		}
	case "notifications/initialized":
		return nil
	case "tools/list":
		return &jsonrpc.Response{JSONRPC: "2.0", Result: map[string]any{"tools": s.toolsList()}, ID: req.ID}
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return &jsonrpc.Response{JSONRPC: "2.0", Error: jsonrpc.ErrMethodNotFound(req.Method), ID: req.ID}
	}
}

func (s *Server) toolsList() []map[string]any {
	names := make([]string, 0, len(s.cfg.Tools))
	for name := range s.cfg.Tools {
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tool := s.cfg.Tools[name]
		inputSchema := tool.InputSchema
		if inputSchema == nil {
			inputSchema = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"name":        name,
			"description": tool.Description,
			"inputSchema": inputSchema,
		})
	}
	return tools
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(req *jsonrpc.Request) *jsonrpc.Response {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonrpc.Response{JSONRPC: "2.0", Error: jsonrpc.ErrInvalidParams(err.Error()), ID: req.ID}
	}

	var args map[string]any
	if len(params.Arguments) == 0 || string(params.Arguments) == "null" {
		args = map[string]any{}
	} else if err := json.Unmarshal(params.Arguments, &args); err != nil {
		return &jsonrpc.Response{JSONRPC: "2.0", Error: jsonrpc.ErrInvalidParams("arguments must be a JSON object"), ID: req.ID}
	}

	result, err := s.call(params.Name, args)
	callResult := map[string]any{
		"content": []map[string]string{{"type": "text", "text": result}},
	}
	if err != nil {
		callResult["content"] = []map[string]string{{"type": "text", "text": err.Error()}}
		callResult["isError"] = true
	}
	return &jsonrpc.Response{JSONRPC: "2.0", Result: callResult, ID: req.ID}
}

func (s *Server) call(toolName string, args map[string]any) (string, error) {
	tool, ok := s.cfg.Tools[toolName]
	if !ok {
		return "", fmt.Errorf("mcp mock %q: unknown tool %q; add a fixture for this tool", s.cfg.Name, toolName)
	}
	for _, response := range tool.Responses {
		if response.matches(args) {
			if response.Error != "" {
				return "", fmt.Errorf("mcp mock %q tool %q fixture error: %s", s.cfg.Name, toolName, response.Error)
			}
			data, err := json.Marshal(response.Return)
			if err != nil {
				return "", fmt.Errorf("mcp mock %q tool %q: marshal response: %w", s.cfg.Name, toolName, err)
			}
			return string(data), nil
		}
	}

	data, _ := json.Marshal(args)
	return "", fmt.Errorf("mcp mock %q: unmatched tool call %q with arguments %s; add a matching fixture response", s.cfg.Name, toolName, string(data))
}

func (r Response) matches(args map[string]any) bool {
	if len(r.Match) == 0 && len(r.MatchSchema) == 0 && len(r.MatchRegex) == 0 {
		return true
	}
	if len(r.Match) > 0 && !exactMatch(r.Match, args) {
		return false
	}
	if len(r.MatchRegex) > 0 && !regexMatch(r.MatchRegex, args) {
		return false
	}
	if len(r.MatchSchema) > 0 && !schemaMatch(r.MatchSchema, args) {
		return false
	}
	return true
}

func exactMatch(want map[string]any, got map[string]any) bool {
	wantJSON, err := json.Marshal(want)
	if err != nil {
		return false
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		return false
	}
	return bytes.Equal(wantJSON, gotJSON)
}

func regexMatch(patterns map[string]string, args map[string]any) bool {
	for field, pattern := range patterns {
		value, ok := args[field]
		if !ok {
			return false
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		if !re.MatchString(fmt.Sprint(value)) {
			return false
		}
	}
	return true
}

func schemaMatch(schemaDoc map[string]any, args map[string]any) bool {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("memory://mcp-mock-schema.json", schemaDoc); err != nil {
		return false
	}
	schema, err := compiler.Compile("memory://mcp-mock-schema.json")
	if err != nil {
		return false
	}
	return schema.Validate(args) == nil
}

func ServeStdio(ctx context.Context, cfg *Config, r io.Reader, w io.Writer, logger *slog.Logger) {
	srv := NewServer(cfg, logger)
	transport := jsonrpc.NewTransport(r, w)
	for {
		req, rawJSON, err := transport.ReadRequest()
		if err != nil {
			if err != io.EOF {
				logger.Debug("mcp mock read error", "error", err)
			}
			return
		}
		resp := srv.HandleRequest(ctx, req)
		if resp == nil || !hasIDField(rawJSON) {
			continue
		}
		if err := transport.WriteResponse(resp); err != nil {
			logger.Debug("mcp mock write error", "error", err)
			return
		}
	}
}

func hasIDField(raw []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	_, exists := obj["id"]
	return exists
}
