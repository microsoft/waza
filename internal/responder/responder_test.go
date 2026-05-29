package responder

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
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
