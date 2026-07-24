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
	require.Equal(t, "echo", stdio.Command)
	// Regression for #449: user-supplied stdio servers must default the tool
	// allowlist to "*" so the bundled Copilot CLI actually spawns them.
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_DefaultsToolsAllowlistForStdio(t *testing.T) {
	// Regression for #449: YAML `mcp_servers` entries loaded as generic maps
	// (mimicking gopkg.in/yaml.v3 output) must produce a stdio config with a
	// non-nil Tools allowlist and a fully populated Command/Args/Env.
	var warnings []string
	servers := ConvertMCPServersWithMocks(map[string]any{
		"probe": map[string]any{
			"command": "python",
			"args":    []any{"mcp_probe_server.py"},
			"env": map[string]any{
				"WAZA_PROBE_MARKER": "on",
			},
		},
	}, nil, "", func(format string, args ...any) {
		warnings = append(warnings, fmtString(format, args...))
	})

	require.Empty(t, warnings)
	require.Contains(t, servers, "probe")
	stdio, ok := servers["probe"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, "python", stdio.Command)
	require.Equal(t, []string{"mcp_probe_server.py"}, stdio.Args)
	require.Equal(t, "on", stdio.Env["WAZA_PROBE_MARKER"])
	require.Equal(t, []string{"*"}, stdio.Tools,
		"user-supplied stdio MCP servers must default Tools to [\"*\"] so the CLI spawns them")
}

func TestConvertMCPServersWithMocks_PreservesExplicitToolsAllowlist(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"probe": map[string]any{
			"command": "python",
			"args":    []any{"mcp_probe_server.py"},
			"tools":   []any{"only_this_tool"},
		},
	}, nil, "", nil)

	stdio, ok := servers["probe"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"only_this_tool"}, stdio.Tools,
		"an explicit tools allowlist from YAML must be preserved verbatim")
}

func TestConvertMCPServersWithMocks_DefaultsToolsAllowlistForHTTP(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"remote": map[string]any{
			"type": "http",
			"url":  "https://example.com/mcp",
		},
	}, nil, "", nil)

	http, ok := servers["remote"].(copilot.MCPHTTPServerConfig)
	require.True(t, ok)
	require.Equal(t, "https://example.com/mcp", http.URL)
	require.Equal(t, []string{"*"}, http.Tools,
		"user-supplied HTTP MCP servers must default Tools to [\"*\"]")
}

func TestConvertMCPServersWithMocks_SkipsStdioMissingCommand(t *testing.T) {
	// Regression for #449: a config with no command silently produced an empty
	// MCPStdioServerConfig that the CLI would ignore. Now we skip it and warn.
	var warnings []string
	servers := ConvertMCPServersWithMocks(map[string]any{
		"broken": map[string]any{
			"args": []any{"mcp_probe_server.py"},
		},
	}, nil, "", func(format string, args ...any) {
		warnings = append(warnings, fmtString(format, args...))
	})

	require.NotContains(t, servers, "broken")
	require.NotEmpty(t, warnings)
	require.Contains(t, warnings[0], "command")
}

func TestConvertMCPServersWithMocks_SkipsHTTPMissingURL(t *testing.T) {
	var warnings []string
	servers := ConvertMCPServersWithMocks(map[string]any{
		"broken": map[string]any{
			"type": "http",
		},
	}, nil, "", func(format string, args ...any) {
		warnings = append(warnings, fmtString(format, args...))
	})

	require.NotContains(t, servers, "broken")
	require.NotEmpty(t, warnings)
	require.Contains(t, warnings[0], "url")
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
