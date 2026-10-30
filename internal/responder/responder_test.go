package responder

import (
	"context"
	"errors"
	"sync"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestDecisionToolsRecordReply(t *testing.T) {
	d := &decisionRecorder{}
	tools := d.tools()
	require.Len(t, tools, 3)

	respond := findTool(t, tools, toolRespond)
	_, err := respond.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"answer": "research-agent"},
	})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionReply, d.decision.Kind)
	require.Equal(t, "research-agent", d.decision.Answer)
}

func TestDecisionToolsRecordStop(t *testing.T) {
	d := &decisionRecorder{}
	stop := findTool(t, d.tools(), toolStop)
	_, err := stop.Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionStop, d.decision.Kind)
}

func TestDecisionToolsRecordAbstain(t *testing.T) {
	d := &decisionRecorder{}
	abstain := findTool(t, d.tools(), toolAbstain)
	_, err := abstain.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"reason": "brief too vague"},
	})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionAbstain, d.decision.Kind)
	require.Equal(t, "brief too vague", d.decision.Reason)
}

type fakeExecutor struct {
	calls   []*execution.ExecutionRequest
	respond func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
}

func (f *fakeExecutor) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	f.calls = append(f.calls, req)
	return f.respond(req)
}

func TestClassifyReply(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": "research-agent"},
			})
			require.NoError(t, err)
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "be research-agent", MaxFollowups: 5}, "gpt-4o")
	d, err := c.Classify(context.Background(), "What is the agent name?")
	require.NoError(t, err)
	require.Equal(t, DecisionReply, d.Kind)
	require.Equal(t, "research-agent", d.Answer)
}

func TestClassifyAbstain(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolAbstain).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"reason": "no info"},
			})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	d, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
	require.Equal(t, DecisionAbstain, d.Kind)
	require.Equal(t, "no info", d.Reason)
}

func TestClassifyWithoutDecision(t *testing.T) {
	executionErr := errors.New("synthetic transport failure")
	for _, tc := range []struct {
		name string
		resp *execution.ExecutionResponse
		err  error
		want string
	}{
		{
			name: "response failure",
			resp: &execution.ExecutionResponse{SessionID: "resp-1", Success: false, ErrorMsg: "synthetic provider rejection"},
			want: "responder execution failed: synthetic provider rejection",
		},
		{
			name: "successful text",
			resp: &execution.ExecutionResponse{SessionID: "resp-1", Success: true, FinalOutput: "synthetic plain text"},
			want: "responder did not call a decision tool",
		},
		{
			name: "failure without detail",
			resp: &execution.ExecutionResponse{SessionID: "resp-1", Success: false},
			want: "responder did not call a decision tool",
		},
		{
			name: "nil response",
			want: "responder did not call a decision tool",
		},
		{
			name: "successful response with error text",
			resp: &execution.ExecutionResponse{Success: true, ErrorMsg: "not a failed execution"},
			want: "responder did not call a decision tool",
		},
		{
			name: "Go error",
			err:  executionErr,
			want: "responder execution failed: synthetic transport failure",
		},
		{
			name: "Go error precedes response failure",
			resp: &execution.ExecutionResponse{Success: false, ErrorMsg: "synthetic provider rejection"},
			err:  executionErr,
			want: "responder execution failed: synthetic transport failure",
		},
	} {
		for _, phase := range []string{"initial", "resumed"} {
			t.Run(tc.name+"/"+phase, func(t *testing.T) {
				exec := &fakeExecutor{
					respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						_, err := findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{})
						require.NoError(t, err)
						return &execution.ExecutionResponse{SessionID: "resp-1", Success: true}, nil
					},
				}
				c := New(exec, models.ResponderConfig{Instructions: "synthetic brief"}, "test-model")
				wantSession, wantCalls := "", 1
				if phase == "resumed" {
					d, err := c.Classify(t.Context(), "Synthetic setup.")
					require.NoError(t, err)
					require.Equal(t, Decision{Kind: DecisionStop}, d)
					wantSession, wantCalls = "resp-1", 2
				}
				exec.respond = func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
					require.Equal(t, wantSession, req.SessionID)
					return tc.resp, tc.err
				}
				d, err := c.Classify(t.Context(), "Synthetic question?")
				require.EqualError(t, err, tc.want)
				if tc.err != nil {
					require.ErrorIs(t, err, tc.err)
				}
				require.Equal(t, Decision{}, d)
				require.Len(t, exec.calls, wantCalls, "classification must not retry")
				if tc.resp != nil && tc.resp.SessionID != "" {
					require.Equal(t, tc.resp.SessionID, c.sessionID)
				}
			})
		}
	}
}

func TestClassifyDecisionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tool    string
		args    map[string]any
		want    Decision
		wantErr string
	}{
		{"reply", toolRespond, map[string]any{"answer": "synthetic answer"}, Decision{Kind: DecisionReply, Answer: "synthetic answer"}, ""},
		{"stop", toolStop, map[string]any{}, Decision{Kind: DecisionStop}, ""},
		{"abstain", toolAbstain, map[string]any{"reason": "synthetic missing fact"}, Decision{Kind: DecisionAbstain, Reason: "synthetic missing fact"}, ""},
		{"conflict", toolStop, map[string]any{}, Decision{}, "responder tool call invalid: responder called responder_stop after a decision was already recorded"},
		{"invalid arguments", toolRespond, map[string]any{"answer": ""}, Decision{}, "responder tool call invalid: responder_reply called with empty answer"},
	} {
		for _, result := range []string{"response failure", "Go and response failure", "nil response"} {
			t.Run(tc.name+"/"+result, func(t *testing.T) {
				exec := &fakeExecutor{
					respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						if tc.name == "conflict" {
							_, err := findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{Arguments: map[string]any{"answer": "first"}})
							require.NoError(t, err)
						}
						_, err := findTool(t, req.Tools, tc.tool).Handler(copilot.ToolInvocation{Arguments: tc.args})
						if tc.wantErr == "" {
							require.NoError(t, err)
						} else {
							require.Error(t, err)
						}
						if result == "nil response" {
							return nil, nil
						}
						resp := &execution.ExecutionResponse{SessionID: "resp-1", Success: false, ErrorMsg: "synthetic provider rejection"}
						if result == "Go and response failure" {
							return resp, errors.New("synthetic transport failure")
						}
						return resp, nil
					},
				}
				c := New(exec, models.ResponderConfig{Instructions: "synthetic brief"}, "test-model")
				for range 2 {
					d, err := c.Classify(t.Context(), "Synthetic question?")
					if tc.wantErr == "" {
						require.NoError(t, err)
					} else {
						require.EqualError(t, err, tc.wantErr)
					}
					require.Equal(t, tc.want, d)
				}
				require.Len(t, exec.calls, 2)
				require.Empty(t, exec.calls[0].SessionID)
				if result != "nil response" {
					require.Equal(t, "resp-1", exec.calls[1].SessionID)
				}
			})
		}
	}
}

func TestClassifyUsesDefaultModelWhenUnset(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			require.Equal(t, "default-model", req.ModelID)
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}

	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "default-model")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
}

func TestClassifyUsesReasoningEffort(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			require.Equal(t, "high", req.ReasoningEffort)
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := NewWithReasoningEffort(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-5", "high")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
}

func TestClassifyPersistsSession(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			require.Len(t, req.Tools, 3)
			for _, name := range []string{toolRespond, toolStop, toolAbstain} {
				require.True(t, findTool(t, req.Tools, name).IsTerminal,
					"%s must end the classification turn, including resumed sessions", name)
			}
			_, _ = findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": "a"},
			})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "INSTR", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q1?")
	require.NoError(t, err)
	_, err = c.Classify(context.Background(), "Q2?")
	require.NoError(t, err)

	require.Len(t, exec.calls, 2)
	require.Empty(t, exec.calls[0].SessionID)
	require.Contains(t, exec.calls[0].Message, "INSTR")
	require.Contains(t, exec.calls[0].Message, "Q1?")
	require.Equal(t, "resp-1", exec.calls[1].SessionID)
	require.NotContains(t, exec.calls[1].Message, "INSTR")
	require.Contains(t, exec.calls[1].Message, "Q2?")
}

func TestClassifyUsesPersistentSession(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)

	require.Len(t, exec.calls, 1)
	require.False(t, exec.calls[0].EphemeralSession,
		"responder must use a persistent (non-ephemeral) session so it can be resumed across turns")
}

// deletingExecutor records the sessions it is asked to delete.
type deletingExecutor struct {
	fakeExecutor
	deleted []string
}

func (d *deletingExecutor) DeleteSession(_ context.Context, sessionID string) error {
	d.deleted = append(d.deleted, sessionID)
	return nil
}

func TestCloseDeletesSession(t *testing.T) {
	exec := &deletingExecutor{}
	exec.respond = func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
		return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)

	require.NoError(t, c.Close(context.Background()))
	require.Equal(t, []string{"resp-1"}, exec.deleted)

	// Close is idempotent: the session id is cleared after the first call.
	require.NoError(t, c.Close(context.Background()))
	require.Equal(t, []string{"resp-1"}, exec.deleted)
}

func TestCloseWithoutSessionIsNoop(t *testing.T) {
	exec := &deletingExecutor{}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	require.NoError(t, c.Close(context.Background()))
	require.Empty(t, exec.deleted)
}

func TestCloseWithoutDeleterIsNoop(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
	require.NoError(t, c.Close(context.Background()))
}

func TestDecisionToolsRejectDuplicateCall(t *testing.T) {
	d := &decisionRecorder{}
	tools := d.tools()
	respond := findTool(t, tools, toolRespond)
	stop := findTool(t, tools, toolStop)

	_, err := respond.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"answer": "first"},
	})
	require.NoError(t, err)

	// A second decision call must be rejected rather than silently
	// overwriting the first decision.
	_, err = stop.Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
	require.Error(t, err)
	require.Error(t, d.err)
	// The first decision is preserved so callers can see what was recorded.
	require.Equal(t, DecisionReply, d.decision.Kind)
	require.Equal(t, "first", d.decision.Answer)
}

func TestDecisionToolsConcurrentCallsRecordOne(t *testing.T) {
	// The Copilot SDK dispatches each tool call on its own goroutine, so a model
	// emitting parallel decision calls in one turn must not race or end up with
	// both decisions partially applied. Run under -race to catch regressions.
	d := &decisionRecorder{}
	tools := d.tools()
	respond := findTool(t, tools, toolRespond)
	stop := findTool(t, tools, toolStop)
	abstain := findTool(t, tools, toolAbstain)

	var wg sync.WaitGroup
	for _, call := range []func(){
		func() { respond.Handler(copilot.ToolInvocation{Arguments: map[string]any{"answer": "a"}}) }, //nolint:errcheck
		func() { stop.Handler(copilot.ToolInvocation{Arguments: map[string]any{}}) },                 //nolint:errcheck
		func() {
			abstain.Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "r"}}) //nolint:errcheck
		},
	} {
		wg.Add(1)
		go func(fn func()) {
			defer wg.Done()
			fn()
		}(call)
	}
	wg.Wait()

	// Exactly one decision wins; the losers are surfaced as a conflict error.
	require.True(t, d.set)
	require.Error(t, d.err)
}

func TestDecisionToolsRejectMalformedArgs(t *testing.T) {
	d := &decisionRecorder{}
	respond := findTool(t, d.tools(), toolRespond)

	// answer must be a string; passing a non-string triggers a decode error
	// that the handler surfaces instead of recording an empty reply.
	_, err := respond.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"answer": map[string]any{"nested": true}},
	})
	require.Error(t, err)
	require.Error(t, d.err)
	require.False(t, d.set)
}

func TestDecisionToolsRejectEmptyReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{}},
		{"blank", map[string]any{"answer": "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &decisionRecorder{}
			respond := findTool(t, d.tools(), toolRespond)
			_, err := respond.Handler(copilot.ToolInvocation{Arguments: tc.args})
			require.Error(t, err)
			require.Error(t, d.err)
			require.False(t, d.set)
		})
	}
}

func TestDecisionToolsRejectEmptyAbstainReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{}},
		{"blank", map[string]any{"reason": "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &decisionRecorder{}
			abstain := findTool(t, d.tools(), toolAbstain)
			_, err := abstain.Handler(copilot.ToolInvocation{Arguments: tc.args})
			require.Error(t, err)
			require.Error(t, d.err)
			require.False(t, d.set)
		})
	}
}

func TestClassifyDuplicateDecisionIsError(t *testing.T) {
	args := map[string]map[string]any{
		toolRespond: {"answer": "a"},
		toolStop:    {},
		toolAbstain: {"reason": "not in the brief"},
	}
	for _, first := range []string{toolRespond, toolStop, toolAbstain} {
		for _, second := range []string{toolRespond, toolStop, toolAbstain} {
			t.Run(first+"/"+second, func(t *testing.T) {
				exec := &fakeExecutor{
					respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						// Both calls were already issued in one classification, not
						// generated by another model round after a terminal decision.
						_, err := findTool(t, req.Tools, first).Handler(copilot.ToolInvocation{Arguments: args[first]})
						require.NoError(t, err)
						_, err = findTool(t, req.Tools, second).Handler(copilot.ToolInvocation{Arguments: args[second]})
						require.EqualError(t, err, "responder called "+second+" after a decision was already recorded")
						return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
					},
				}
				c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
				d, err := c.Classify(context.Background(), "Q?")
				require.EqualError(t, err, "responder tool call invalid: responder called "+second+" after a decision was already recorded")
				require.Equal(t, Decision{}, d, "conflicts must not return the first decision")
				require.Len(t, exec.calls, 1)
			})
		}
	}
}

func TestClassifyMalformedArgsIsError(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": 42},
			})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.Error(t, err)
	require.Contains(t, err.Error(), "responder tool call invalid")
}

func findTool(t *testing.T, tools []copilot.Tool, name string) copilot.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found", name)
	return copilot.Tool{}
}
