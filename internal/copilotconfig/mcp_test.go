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
	// Regression for issue #449: the bundled Copilot CLI silently drops MCP
	// servers whose tools allowlist is omitted, so we must default to "*".
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_StdioDefaultsToolsAllowlistWhenTypeOmitted(t *testing.T) {
	// Users often omit the "type" field for stdio servers; the default branch
	// must still install the "*" allowlist workaround for issue #449.
	servers := ConvertMCPServersWithMocks(map[string]any{
		"probe": map[string]any{"command": "bash", "args": []any{"-c", "echo hi"}},
	}, nil, "", nil)

	stdio, ok := servers["probe"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_HTTPDefaultsToolsAllowlist(t *testing.T) {
	servers := ConvertMCPServersWithMocks(map[string]any{
		"remote": map[string]any{"type": "http", "url": "https://example.com/mcp"},
	}, nil, "", nil)

	http, ok := servers["remote"].(copilot.MCPHTTPServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, http.Tools)
}

func TestConvertMCPServersWithMocks_PreservesExplicitToolsAllowlist(t *testing.T) {
	// A user-supplied named allowlist must be respected verbatim; do not clobber
	// with "*".
	servers := ConvertMCPServersWithMocks(map[string]any{
		"scoped": map[string]any{
			"type":    "stdio",
			"command": "echo",
			"tools":   []any{"list_issues", "get_pr"},
		},
	}, nil, "", nil)

	stdio, ok := servers["scoped"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"list_issues", "get_pr"}, stdio.Tools)
}

func TestConvertMCPServersWithMocks_PreservesExplicitEmptyToolsAllowlist(t *testing.T) {
	// An explicit empty allowlist is the SDK's "expose no tools" opt-out; the
	// #449 default must not overwrite it.
	servers := ConvertMCPServersWithMocks(map[string]any{
		"muted": map[string]any{
			"type":    "stdio",
			"command": "echo",
			"tools":   []any{},
		},
	}, nil, "", nil)

	stdio, ok := servers["muted"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.NotNil(t, stdio.Tools)
	require.Empty(t, stdio.Tools)
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
