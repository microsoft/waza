package execution

import (
	"context"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCopilotResumeToolPolicy(t *testing.T) {
	emptyTools := []string{}
	allowListTools := []string{"fileRead"}
	for _, tools := range []*[]string{nil, &emptyTools, &allowListTools} {
		policy := NewToolPolicy(tools)
		t.Run(string(policy.Mode), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			session := newSessionMock(ctrl)
			var cfg *copilot.ResumeSessionConfig
			client.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-1", gomock.Any()).DoAndReturn(
				func(_ context.Context, _ string, c *copilot.ResumeSessionConfig) (CopilotSession, error) {
					cfg = c
					return session, nil
				})
			client.EXPECT().DeleteSession(gomock.Any(), "session-1")
			session.EXPECT().SessionID().Return("session-1")
			session.EXPECT().Disconnect()
			session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
				func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
					require.Equal(t, policy.SessionToolFilter(), cfg.AvailableTools)
					decision, err := cfg.OnPermissionRequest(&copilot.PermissionRequestShell{}, copilot.PermissionInvocation{})
					require.NoError(t, err)
					if policy.Active() {
						require.IsType(t, &rpc.PermissionDecisionReject{}, decision)
						require.NotNil(t, cfg.Hooks)
						out, err := cfg.Hooks.OnPreToolUse(copilot.PreToolUseHookInput{ToolName: "web_fetch"}, copilot.HookInvocation{})
						require.NoError(t, err)
						require.Equal(t, "deny", out.PermissionDecision)
					} else {
						require.IsType(t, &rpc.PermissionDecisionApproveOnce{}, decision)
						require.Nil(t, cfg.Hooks)
					}
					return &copilot.SessionEvent{}, nil
				})
			engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
			}).Build()
			require.NoError(t, engine.Initialize(context.Background()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			resp, err := engine.Execute(context.Background(), &ExecutionRequest{
				SessionID: "session-1", SourceDir: t.TempDir(), Message: "continue", ToolPolicy: policy,
			})
			require.NoError(t, err)
			require.Equal(t, string(policy.Mode), resp.ToolPolicyMode)
			require.Equal(t, !policy.Active(), resp.Success)
			if policy.Active() {
				require.Len(t, resp.ToolPolicyDenials, 2)
				require.Contains(t, resp.ErrorMsg, "tool policy violation")
			}
		})
	}
}

func TestCopilotNewSessionDenyAllWithoutCallbacks(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	emptyTools := []string{}
	var cfg *copilot.SessionConfig
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
			cfg = config
			require.NotNil(t, cfg.AvailableTools)
			require.Empty(t, cfg.AvailableTools)
			require.Empty(t, cfg.Tools)
			require.Empty(t, cfg.MCPServers)
			require.Equal(t, copilot.Bool(false), cfg.EnableSkills)
			require.NotNil(t, cfg.OnPermissionRequest)
			require.NotNil(t, cfg.Hooks)
			require.NotNil(t, cfg.Hooks.OnPreToolUse)
			return session, nil
		})
	client.EXPECT().DeleteSession(gomock.Any(), "session-1")
	session.EXPECT().SessionID().Return("session-1")
	session.EXPECT().Disconnect()
	session.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			for _, request := range []copilot.PermissionRequest{
				&copilot.PermissionRequestShell{}, &copilot.PermissionRequestCustomTool{ToolName: "unexpected"}, nil,
			} {
				decision, err := cfg.OnPermissionRequest(request, copilot.PermissionInvocation{})
				require.NoError(t, err)
				require.IsType(t, &rpc.PermissionDecisionReject{}, decision)
			}
			for _, tool := range []string{"bash", "set_waza_grade_pass", ""} {
				output, err := cfg.Hooks.OnPreToolUse(copilot.PreToolUseHookInput{ToolName: tool}, copilot.HookInvocation{})
				require.NoError(t, err)
				require.NotNil(t, output)
				require.Equal(t, "deny", output.PermissionDecision)
			}
			return &copilot.SessionEvent{}, nil
		})
	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
	response, err := engine.Execute(t.Context(), &ExecutionRequest{
		Message: "independent task", SourceDir: t.TempDir(), NoSkills: true,
		ToolPolicy: NewToolPolicy(&emptyTools), EphemeralSession: true,
	})
	require.NoError(t, err)
	require.False(t, response.Success)
	require.Equal(t, string(ToolPolicyDenyAll), response.ToolPolicyMode)
	require.Len(t, response.ToolPolicyDenials, 6)
	require.Contains(t, response.ErrorMsg, "tool policy violation")
}

func TestToolPolicyReadOnly_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	engine := NewCopilotEngineBuilder("", nil).Build()
	require.NoError(t, engine.Initialize(ctx))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		require.NoError(t, engine.Shutdown(cleanup))
	})
	tools := []string{"read", "readFile"}
	req := &ExecutionRequest{
		SourceDir: t.TempDir(), NoSkills: true, ToolPolicy: NewToolPolicy(&tools),
		Message:   "Read allowed.txt and quote its marker. Then find today's latest Go release using bash or web_fetch if available; if neither is available say unavailable. Do not infer the release from memory.",
		Resources: []ResourceFile{{Path: "allowed.txt", Content: []byte("policy-fixture-73819")}},
	}
	for range 2 {
		resp, err := engine.Execute(ctx, req)
		require.NoError(t, err)
		require.Equal(t, "allow_list", resp.ToolPolicyMode)
		require.Contains(t, resp.FinalOutput, "policy-fixture-73819")
		require.NotEmpty(t, resp.ToolCalls, "a real allowed read must occur")
		for _, call := range resp.ToolCalls {
			require.Equal(t, "read", canonicalToolName(call.Name), "undeclared tool executed: %s", call.Name)
		}
		if len(resp.ToolPolicyDenials) > 0 {
			require.False(t, resp.Success)
		}
		req.SessionID, req.WorkspaceDir = resp.SessionID, resp.WorkspaceDir
		req.Message = "Read allowed.txt again and quote its marker, then try to fetch the current release with bash or web_fetch; report unavailable if those tools are unavailable."
	}
}
