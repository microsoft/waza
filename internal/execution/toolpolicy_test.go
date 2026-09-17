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
		require.ElementsMatch(t, []string{"read", "readFile"}, p.SessionToolFilter())
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
		{"mcp tool", &copilot.PermissionRequestMCP{ToolName: "SomeTool", ServerName: "srv"}, "sometool", true},
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
	require.Equal(t, "other", denials[3].Tool)
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
