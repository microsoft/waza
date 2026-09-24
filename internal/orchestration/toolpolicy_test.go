package orchestration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type policyTestEngine struct {
	*execution.MockEngine
	execute func(*execution.ExecutionRequest) *execution.ExecutionResponse
}

func (e *policyTestEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return e.execute(req), nil
}

func TestToolPolicyLaterTurns(t *testing.T) {
	for _, responder := range []bool{false, true} {
		t.Run(map[bool]string{false: "static", true: "responder"}[responder], func(t *testing.T) {
			dir := t.TempDir()
			writeAgentFile(t, dir, "reader.agent.md", "---\nname: reader\ntools: [fileRead]\n---\n")
			spec := &models.EvalSpec{SkillName: "reader", Config: models.Config{SkillPaths: []string{dir}, TimeoutSec: 30}}
			calls := 0
			eng := &policyTestEngine{MockEngine: execution.NewMockEngine("mock")}
			eng.execute = func(req *execution.ExecutionRequest) *execution.ExecutionResponse {
				calls++
				require.Equal(t, execution.ToolPolicyAllowList, req.ToolPolicy.Mode)
				require.False(t, req.ToolPolicy.IsAllowed("bash"))
				resp := &execution.ExecutionResponse{
					Success: true, SessionID: "session", WorkspaceDir: dir, ToolPolicyMode: "allow_list",
				}
				if calls > 1 {
					require.Equal(t, "session", req.SessionID)
					resp.Success = false
					resp.ErrorMsg = "tool policy violation"
					resp.ToolPolicyDenials = []execution.ToolPolicyDenial{{Tool: "bash", Kind: "shell", Reason: "undeclared"}}
				}
				return resp
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec), eng, WithSkipGraders())
			tc := &models.TestCase{Stimulus: models.TaskStimulus{Message: "read", FollowUps: []string{"try bash", "not sent"}}}
			var digest models.SessionDigest
			if responder {
				req, err := runner.buildExecutionRequest(tc)
				require.NoError(t, err)
				resp, err := eng.Execute(context.Background(), req)
				require.NoError(t, err)
				require.False(t, runner.sendResponderReply(context.Background(), tc, resp, "try bash", 1))
				require.False(t, resp.Success)
				digest = runner.buildSessionDigest(resp)
			} else {
				run := runner.executeRun(context.Background(), tc, 1)
				require.NotEqual(t, models.StatusPassed, run.Status)
				digest = run.SessionDigest
			}
			require.Equal(t, 2, calls)
			require.Equal(t, "allow_list", digest.ToolPolicyMode)
			require.Len(t, digest.ToolPolicyDenials, 1)
			data, err := json.Marshal(digest)
			require.NoError(t, err)
			require.Contains(t, string(data), `"tool_policy_denials":[{"tool":"bash","kind":"shell","reason":"undeclared"}]`)
		})
	}
}

func TestImplicitPolicyGraderUsesTaskAgent(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "reader.agent.md", "---\nname: reader\ntools: [fileRead]\n---\n")
	spec := &models.EvalSpec{SkillName: "reader"}
	runner := NewEvalRunner(config.NewEvalConfig(spec), execution.NewMockEngine("mock"))
	tc := &models.TestCase{SkillPaths: []string{dir}}
	for _, name := range []string{"view", "builtin:readFile", "bash"} {
		results, err := runner.runGraders(context.Background(), tc, &graders.Context{
			Session: &models.SessionDigest{ToolCalls: []models.ToolCall{{Name: name}}},
		})
		require.NoError(t, err)
		require.Equal(t, name != "bash", results["agent_tools_implicit"].Passed)
	}
	require.Empty(t, spec.Graders, "implicit graders must not leak into another task or baseline pass")
}
