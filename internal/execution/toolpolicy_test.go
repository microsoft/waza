package execution

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/stretchr/testify/require"
)

func TestNewToolPolicy_TriState(t *testing.T) {
	t.Run("nil tools is unrestricted", func(t *testing.T) {
		p := NewToolPolicy(nil)
		require.Equal(t, ToolPolicyUnrestricted, p.Mode)
		require.True(t, p.IsAllowed("bash"))
		require.True(t, p.IsAllowed("anything"))
		require.Nil(t, p.SessionToolFilter())
		require.False(t, p.Active())
	})

	t.Run("empty tools is deny-all", func(t *testing.T) {
		empty := []string{}
		p := NewToolPolicy(&empty)
		require.Equal(t, ToolPolicyDenyAll, p.Mode)
		require.False(t, p.IsAllowed("bash"))
		require.False(t, p.IsAllowed("read"))
		require.NotNil(t, p.SessionToolFilter())
		require.Empty(t, p.SessionToolFilter())
		require.True(t, p.Active())
	})

	t.Run("populated tools is an allow-list", func(t *testing.T) {
		declared := []string{"read", "readFile"}
		p := NewToolPolicy(&declared)
		require.Equal(t, ToolPolicyAllowList, p.Mode)
		require.True(t, p.IsAllowed("read"))
		require.True(t, p.IsAllowed("READ"))
		require.True(t, p.IsAllowed("readfile"))
		require.False(t, p.IsAllowed("bash"))
		require.False(t, p.IsAllowed("web_fetch"))
		require.ElementsMatch(t, []string{"builtin:view", "builtin:view"}, p.SessionToolFilter())
		require.True(t, p.Active())
	})
}

func TestToolPolicy_ExactNotSubstringMatch(t *testing.T) {
	declared := []string{"bash"}
	p := NewToolPolicy(&declared)
	// "bashful" must not match "bash" (exact match, not prefix/substring).
	require.False(t, p.IsAllowed("bashful"))
}

func TestToolPolicy_CanonicalPrefixStripping(t *testing.T) {
	declared := []string{"bash", "some_mcp_tool"}
	p := NewToolPolicy(&declared)
	require.True(t, p.IsAllowed("builtin:bash"))
	require.True(t, p.IsAllowed("mcp:some_mcp_tool"))
	// Declaring one MCP tool must not implicitly allow every tool from that
	// server: a different tool name from the same server must still be denied.
	require.False(t, p.IsAllowed("mcp:other_tool_same_server"))
}

func TestToolPolicy_NilReceiver(t *testing.T) {
	var p *ToolPolicy
	require.True(t, p.IsAllowed("anything"))
	require.Nil(t, p.SessionToolFilter())
	require.False(t, p.Active())
}

func TestCanonicalPermissionToolName(t *testing.T) {
	cases := []struct {
		name    string
		request copilot.PermissionRequest
		want    string
		wantOK  bool
	}{
		{"custom tool", &copilot.PermissionRequestCustomTool{ToolName: "task"}, "task", true},
		{"mcp tool", &copilot.PermissionRequestMCP{ToolName: "SomeTool", ServerName: "srv"}, "srv-sometool", true},
		{"mcp without server", &copilot.PermissionRequestMCP{ToolName: "SomeTool"}, "", false},
		{"empty custom", &copilot.PermissionRequestCustomTool{}, "", false},
		{"hook", &copilot.PermissionRequestHook{ToolName: "bash"}, "bash", true},
		{"factory/subagent", &copilot.PermissionRequestFactory{Name: "researcher"}, "researcher", true},
		{"factory/subagent unnamed", &copilot.PermissionRequestFactory{}, "task", true},
		{"read", &copilot.PermissionRequestRead{Path: "/tmp/x"}, "read", true},
		{"write", &copilot.PermissionRequestWrite{FileName: "/tmp/x"}, "write", true},
		{"shell", &copilot.PermissionRequestShell{FullCommandText: "ls"}, "bash", true},
		{"url", &copilot.PermissionRequestURL{URL: "https://example.com"}, "fetch", true},
		{"memory", &copilot.PermissionRequestMemory{Fact: "x"}, "memory", true},
		{"unrecognized", &copilot.RawPermissionRequest{Discriminator: "something-new"}, "", false},
		{"extension management", &copilot.PermissionRequestExtensionManagement{Operation: "reload"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := canonicalPermissionToolName(tc.request)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEnforceToolPolicy_AllowListApprovesDeclaredDeniesOthers(t *testing.T) {
	declared := []string{"read"}
	policy := NewToolPolicy(&declared)
	recorder := newToolPolicyRecorder()
	handler := enforceToolPolicy(policy, recorder, allowAllTools)

	// Declared tool: forwarded to next (allowAllTools approves).
	decision, err := handler(&copilot.PermissionRequestRead{Path: "/tmp/x"}, copilot.PermissionInvocation{})
	require.NoError(t, err)
	_, approved := decision.(*rpc.PermissionDecisionApproveOnce)
	require.True(t, approved)
	require.Empty(t, recorder.snapshot())

	// Undeclared built-in tool (shell/bash): denied before execution.
	decision, err = handler(&copilot.PermissionRequestShell{FullCommandText: "ls"}, copilot.PermissionInvocation{})
	require.NoError(t, err)
	rej, denied := decision.(*rpc.PermissionDecisionReject)
	require.True(t, denied)
	require.NotNil(t, rej.Feedback)

	// Undeclared web tool: denied before execution.
	_, err = handler(&copilot.PermissionRequestURL{URL: "https://example.com"}, copilot.PermissionInvocation{})
	require.NoError(t, err)

	// Subagent/task tool: denied before execution.
	_, err = handler(&copilot.PermissionRequestFactory{Name: "sub"}, copilot.PermissionInvocation{})
	require.NoError(t, err)

	// MCP tool with an undeclared name: denied even though a different tool
	// on the same conceptual server might be allowed elsewhere.
	_, err = handler(&copilot.PermissionRequestMCP{ToolName: "other", ServerName: "srv"}, copilot.PermissionInvocation{})
	require.NoError(t, err)

	// Unrecognized/unparseable request kind: fail-closed denied.
	_, err = handler(&copilot.RawPermissionRequest{Discriminator: "mystery"}, copilot.PermissionInvocation{})
	require.NoError(t, err)

	denials := recorder.snapshot()
	require.Len(t, denials, 5)
	require.Equal(t, "bash", denials[0].Tool)
	require.Equal(t, "fetch", denials[1].Tool)
	require.Equal(t, "sub", denials[2].Tool)
	require.Equal(t, "srv-other", denials[3].Tool)
	require.Equal(t, "", denials[4].Tool)
}

func TestEnforceToolPolicy_ReadFileAliasAllowsActualReadRequest(t *testing.T) {
	// Regression test: `.agent.md` `tools: [readFile]` must allow an actual
	// PermissionRequestRead (which the SDK does not tag with the "readFile"
	// name the agent declared), not just pass the session AvailableTools
	// filter while denying every real read at the permission-handler layer.
	declared := []string{"readFile"}
	policy := NewToolPolicy(&declared)
	recorder := newToolPolicyRecorder()
	handler := enforceToolPolicy(policy, recorder, allowAllTools)

	decision, err := handler(&copilot.PermissionRequestRead{Path: "/tmp/x"}, copilot.PermissionInvocation{})
	require.NoError(t, err)
	_, approved := decision.(*rpc.PermissionDecisionApproveOnce)
	require.True(t, approved)
	require.Empty(t, recorder.snapshot())

	// Unrelated tool remains denied.
	_, err = handler(&copilot.PermissionRequestShell{FullCommandText: "ls"}, copilot.PermissionInvocation{})
	require.NoError(t, err)
	require.Len(t, recorder.snapshot(), 1)
}

func TestEnforceToolPolicy_DenyAllDeniesEverything(t *testing.T) {
	empty := []string{}
	policy := NewToolPolicy(&empty)
	recorder := newToolPolicyRecorder()
	handler := enforceToolPolicy(policy, recorder, allowAllTools)

	decision, err := handler(&copilot.PermissionRequestRead{Path: "/tmp/x"}, copilot.PermissionInvocation{})
	require.NoError(t, err)
	_, denied := decision.(*rpc.PermissionDecisionReject)
	require.True(t, denied)

	require.Len(t, recorder.snapshot(), 1)
}

func TestEnforceToolPolicy_UnrestrictedNeverWrapped(t *testing.T) {
	// Sanity check: an unrestricted policy is inactive, so callers should
	// not wrap the handler at all (see CopilotEngine.Execute). Verify the
	// policy itself would approve everything if it were consulted directly.
	policy := NewToolPolicy(nil)
	require.False(t, policy.Active())
	require.True(t, policy.IsAllowed("bash"))
}

func TestToolPolicyAliasesAndMCP(t *testing.T) {
	for _, tc := range []struct {
		declared string
		request  copilot.PermissionRequest
		native   string
	}{
		{"fileRead", &copilot.PermissionRequestRead{}, "builtin:view"},
		{"fileWrite", &copilot.PermissionRequestWrite{}, "builtin:edit"},
		{"runCommand", &copilot.PermissionRequestShell{}, "builtin:bash"},
		{"web_fetch", &copilot.PermissionRequestURL{}, "builtin:web_fetch"},
		{"mcp:github-list_issues", &copilot.PermissionRequestMCP{ServerName: "github", ToolName: "list_issues"}, "mcp:github-list_issues"},
		{"custom:CodeSearch", &copilot.PermissionRequestCustomTool{ToolName: "CodeSearch"}, "custom:CodeSearch"},
		{"custom:view", &copilot.PermissionRequestCustomTool{ToolName: "view"}, "custom:view"},
	} {
		t.Run(tc.declared, func(t *testing.T) {
			tools := []string{tc.declared}
			p := NewToolPolicy(&tools)
			require.Equal(t, []string{tc.native}, p.SessionToolFilter())
			rec := newToolPolicyRecorder()
			decision, err := enforceToolPolicy(p, rec, allowAllTools)(tc.request, copilot.PermissionInvocation{})
			require.NoError(t, err)
			require.IsType(t, &rpc.PermissionDecisionApproveOnce{}, decision)
			require.Empty(t, rec.snapshot())
			require.False(t, p.IsAllowed("other-list_issues"))
		})
	}
}

func TestEnforceToolCall(t *testing.T) {
	for _, tools := range [][]string{{}, {"fileRead"}, {"*"}} {
		p := NewToolPolicy(&tools)
		rec := newToolPolicyRecorder()
		hook := enforceToolCall(p, rec)
		for _, name := range []string{"view", "bash", "web_fetch", "task", ""} {
			output, err := hook(copilot.PreToolUseHookInput{ToolName: name}, copilot.HookInvocation{})
			require.NoError(t, err)
			if p.IsAllowed(name) && name != "" {
				require.Nil(t, output)
			} else {
				require.Equal(t, "deny", output.PermissionDecision)
			}
		}
		require.NotEmpty(t, rec.snapshot())
	}
}
