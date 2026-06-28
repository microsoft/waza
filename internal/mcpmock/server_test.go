package mcpmock

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFromEvalConfig_LoadsFixtureDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, osWriteFile(filepath.Join(dir, "list_issues.json"), `{
		"description": "List issues",
		"responses": [
			{"match": {"owner": "octocat"}, "return": {"issues": [{"number": 1}]}}
		]
	}`))

	cfg, err := FromEvalConfig(models.MCPMockConfig{Name: "github", Fixtures: dir}, "")
	require.NoError(t, err)
	require.Contains(t, cfg.Tools, "list_issues")
	require.Equal(t, "List issues", cfg.Tools["list_issues"].Description)
}

func TestFromEvalConfig_RejectsInvalidMatchers(t *testing.T) {
	_, err := FromEvalConfig(models.MCPMockConfig{
		Name: "github",
		Tools: map[string]models.MCPMockTool{
			"list_issues": {
				Responses: []models.MCPMockResponse{{MatchRegex: map[string]string{"owner": "["}, Return: map[string]any{"issues": []any{}}}},
			},
		},
	}, "")
	require.ErrorContains(t, err, "invalid regex")

	_, err = FromEvalConfig(models.MCPMockConfig{
		Name: "github",
		Tools: map[string]models.MCPMockTool{
			"list_issues": {
				Responses: []models.MCPMockResponse{{MatchSchema: map[string]any{"type": 42}, Return: map[string]any{"issues": []any{}}}},
			},
		},
	}, "")
	require.ErrorContains(t, err, "match_schema is invalid")
}

func TestServerToolsCallMatchesExactSchemaAndRegex(t *testing.T) {
	srv := NewServer(&Config{
		Name: "github",
		Tools: map[string]Tool{
			"list_issues": {
				Responses: []Response{
					{Match: map[string]any{"owner": "octocat"}, Return: map[string]any{"source": "exact"}},
					{MatchRegex: map[string]string{"owner": "^micro"}, Return: map[string]any{"source": "regex"}},
					{MatchSchema: map[string]any{
						"type":     "object",
						"required": []any{"repo"},
						"properties": map[string]any{
							"repo": map[string]any{"const": "waza"},
						},
					}, Return: map[string]any{"source": "schema"}},
				},
			},
		},
	}, slog.Default())

	requireToolJSON(t, srv, "list_issues", map[string]any{"owner": "octocat"}, `"source":"exact"`)
	requireToolJSON(t, srv, "list_issues", map[string]any{"owner": "microsoft"}, `"source":"regex"`)
	requireToolJSON(t, srv, "list_issues", map[string]any{"repo": "waza"}, `"source":"schema"`)
}

func TestServerUnknownAndUnmatchedCallsReturnMCPErrorResult(t *testing.T) {
	srv := NewServer(&Config{
		Name: "github",
		Tools: map[string]Tool{
			"list_issues": {Responses: []Response{{Match: map[string]any{"owner": "octocat"}, Return: map[string]any{"ok": true}}}},
		},
	}, slog.Default())

	requireToolError(t, srv, "missing", map[string]any{}, `unknown tool "missing"`)
	requireToolError(t, srv, "list_issues", map[string]any{"owner": "microsoft"}, `unmatched tool call "list_issues"`)
}

func TestToolsListIncludesConfiguredSchemas(t *testing.T) {
	srv := NewServer(&Config{
		Name: "github",
		Tools: map[string]Tool{
			"list_issues": {
				Description: "List issues",
				InputSchema: map[string]any{
					"type": "object",
				},
				Responses: []Response{{Return: map[string]any{"ok": true}}},
			},
		},
	}, slog.Default())

	req := &jsonrpc.Request{JSONRPC: "2.0", Method: "tools/list", ID: json.RawMessage(`1`)}
	resp := srv.HandleRequest(context.Background(), req)
	require.Nil(t, resp.Error)
	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	require.Contains(t, string(data), `"name":"list_issues"`)
	require.Contains(t, string(data), `"description":"List issues"`)
	require.Contains(t, string(data), `"inputSchema"`)
}

func requireToolJSON(t *testing.T, srv *Server, name string, args map[string]any, want string) {
	t.Helper()
	result := toolCall(t, srv, name, args)
	require.False(t, result.IsError, "tool returned error: %v", result.Content)
	require.Contains(t, result.Content[0].Text, want)
}

func requireToolError(t *testing.T, srv *Server, name string, args map[string]any, want string) {
	t.Helper()
	result := toolCall(t, srv, name, args)
	require.True(t, result.IsError, "expected tool error")
	require.Contains(t, result.Content[0].Text, want)
}

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func toolCall(t *testing.T, srv *Server, name string, args map[string]any) callResult {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	require.NoError(t, err)
	params, err := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(argsJSON)})
	require.NoError(t, err)
	resp := srv.HandleRequest(context.Background(), &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`1`),
	})
	require.Nil(t, resp.Error)
	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result callResult
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Content, 1)
	return result
}

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
