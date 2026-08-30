package copilotconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestConvertMCPServersWithMocks_AddsHermeticStdioServer(t *testing.T) {
	var warnings []string
	servers := ConvertMCPServersWithMocks(nil, []models.MCPMockConfig{{
		Name: "github",
		Tools: map[string]models.MCPMockTool{
			"list_issues": {
				Responses: []models.MCPMockResponse{{Match: map[string]any{"owner": "octocat"}, Return: map[string]any{"issues": []any{}}}},
			},
		},
	}}, t.TempDir(), func(format string, args ...any) {
		warnings = append(warnings, fmtString(format, args...))
	})

	require.Empty(t, warnings)
	require.Contains(t, servers, "github")
	stdio, ok := servers["github"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.NotEmpty(t, stdio.Command)
	require.Equal(t, "1", stdio.Env["WAZA_NO_UPDATE_CHECK"])
	require.Equal(t, []string{"*"}, stdio.Tools)
	require.Len(t, stdio.Args, 3)
	require.Equal(t, "__mcp-mock", stdio.Args[0])
	require.Equal(t, "--config-file", stdio.Args[1])

	data, err := os.ReadFile(stdio.Args[2])
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(stdio.Args[2]) })
	var cfg mcpmock.Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.Equal(t, "github", cfg.Name)
	require.Contains(t, cfg.Tools, "list_issues")
}

func TestConvertMCPServersWithMocks_PreservesRegularServers(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"regular": map[string]any{"type": "stdio", "command": "echo"},
	}, nil, "", nil)

	require.Contains(t, servers, "regular")
	stdio, ok := servers["regular"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_PreservesExplicitToolAllowlist(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"stdio": map[string]any{
			"type":    "stdio",
			"command": "echo",
			"tools":   []any{"one", "two"},
		},
		"http": map[string]any{
			"type":  "http",
			"url":   "https://example.test/mcp",
			"tools": []any{},
		},
		"stdio-empty": map[string]any{
			"type":    "stdio",
			"command": "cat",
			"tools":   []any{},
		},
	}, nil, "", nil)

	stdio, ok := servers["stdio"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"one", "two"}, stdio.Tools)

	http, ok := servers["http"].(copilot.MCPHTTPServerConfig)
	require.True(t, ok)
	require.Empty(t, http.Tools)

	stdioEmpty, ok := servers["stdio-empty"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Empty(t, stdioEmpty.Tools)
}

func TestConvertMCPServersWithMocks_DefaultsHTTPToolsToAll(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"remote": map[string]any{
			"type": "http",
			"url":  "https://example.test/mcp",
		},
		"stdio-null": map[string]any{
			"type":    "stdio",
			"command": "echo",
			"tools":   nil,
		},
	}, nil, "", nil)

	http, ok := servers["remote"].(copilot.MCPHTTPServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, http.Tools)

	stdio, ok := servers["stdio-null"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_InvalidMockDisablesLiveServerFallback(t *testing.T) {
	var warnings []string
	servers := ConvertMCPServersWithMocks(map[string]any{
		"github": map[string]any{"type": "stdio", "command": "echo"},
	}, []models.MCPMockConfig{{
		Name:     " github ",
		Fixtures: "missing",
	}}, t.TempDir(), func(format string, args ...any) {
		warnings = append(warnings, fmtString(format, args...))
	})

	require.NotEmpty(t, warnings)
	require.NotContains(t, servers, "github")
}

func fmtString(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}
