package responder

import (
	"context"
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

func TestClassifyNoDecisionToolIsError(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.Error(t, err)
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

func TestClassifyPersistsSession(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
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
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			// The model calls reply first, then stop in the same turn.
			_, _ = findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": "a"},
			})
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.Error(t, err)
	require.Contains(t, err.Error(), "responder tool call invalid")
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
