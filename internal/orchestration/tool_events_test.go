package orchestration

import (
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func mkEvent(ts time.Time, data copilot.SessionEventData) copilot.SessionEvent {
	return copilot.SessionEvent{Timestamp: ts, Data: data}
}

func TestBuildToolEvents_EmptyReturnsNil(t *testing.T) {
	require.Nil(t, buildToolEvents(nil))
	require.Nil(t, buildToolEvents([]copilot.SessionEvent{}))
}

func TestBuildToolEvents_PairsStartAndCompleteByID(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{
			ToolCallID: "call-a",
			ToolName:   "bash",
			Arguments:  map[string]any{"command": "go test ./..."},
		}),
		mkEvent(t0.Add(150*time.Millisecond), &copilot.ToolExecutionCompleteData{
			ToolCallID: "call-a",
			Success:    true,
			Result:     &copilot.ToolExecutionCompleteResult{Content: "ok"},
		}),
	}

	out := buildToolEvents(events)
	require.Len(t, out, 1)
	te := out[0]
	require.Equal(t, "call-a", te.ToolCallID)
	require.Equal(t, "bash", te.ToolName)
	require.Equal(t, 1, te.Sequence)
	require.Equal(t, 1, te.Turn)
	require.True(t, te.Success)
	require.Equal(t, int64(150), te.DurationMs)
	require.Equal(t, "", te.Error)

	argsMap, ok := te.Args.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "go test ./...", argsMap["command"])
}

func TestBuildToolEvents_TurnIncrementsAtAssistantMessage(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		// pre-turn tool call lands in turn 1
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "c0", ToolName: "view"}),
		mkEvent(t0.Add(10*time.Millisecond), &copilot.ToolExecutionCompleteData{ToolCallID: "c0", Success: true}),
		mkEvent(t0.Add(20*time.Millisecond), &copilot.AssistantMessageData{Content: "first turn"}),
		mkEvent(t0.Add(30*time.Millisecond), &copilot.ToolExecutionStartData{ToolCallID: "c1", ToolName: "bash"}),
		mkEvent(t0.Add(40*time.Millisecond), &copilot.ToolExecutionCompleteData{ToolCallID: "c1", Success: true}),
		mkEvent(t0.Add(50*time.Millisecond), &copilot.AssistantMessageData{Content: "second turn"}),
		mkEvent(t0.Add(60*time.Millisecond), &copilot.ToolExecutionStartData{ToolCallID: "c2", ToolName: "edit"}),
		mkEvent(t0.Add(70*time.Millisecond), &copilot.ToolExecutionCompleteData{ToolCallID: "c2", Success: true}),
	}

	out := buildToolEvents(events)
	require.Len(t, out, 3)
	require.Equal(t, 1, out[0].Turn)
	require.Equal(t, 1, out[1].Turn)
	require.Equal(t, 2, out[2].Turn)

	// Sequence is monotonically increasing in start order.
	require.Equal(t, 1, out[0].Sequence)
	require.Equal(t, 2, out[1].Sequence)
	require.Equal(t, 3, out[2].Sequence)
}

func TestBuildToolEvents_MissingCompleteStaysPending(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "orphan", ToolName: "bash"}),
	}
	out := buildToolEvents(events)
	require.Len(t, out, 1)
	require.Equal(t, "orphan", out[0].ToolCallID)
	require.False(t, out[0].Success)
	require.Equal(t, int64(0), out[0].DurationMs)
}

func TestBuildToolEvents_FailureCapturesErrorMessage(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "x", ToolName: "bash"}),
		mkEvent(t0.Add(5*time.Millisecond), &copilot.ToolExecutionCompleteData{
			ToolCallID: "x",
			Success:    false,
			Result:     &copilot.ToolExecutionCompleteResult{Content: "boom"},
		}),
	}
	out := buildToolEvents(events)
	require.Len(t, out, 1)
	require.False(t, out[0].Success)
	require.NotEmpty(t, out[0].Error, "Error should be populated when Success=false")
}

func TestBuildToolEvents_IgnoresUnknownCompleteIDs(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "a", ToolName: "bash"}),
		mkEvent(t0.Add(1*time.Millisecond), &copilot.ToolExecutionCompleteData{
			ToolCallID: "ghost",
			Success:    true,
		}),
	}
	out := buildToolEvents(events)
	require.Len(t, out, 1)
	require.Equal(t, "a", out[0].ToolCallID)
	require.False(t, out[0].Success, "matching event was the ghost ID; original 'a' has no complete")
}

func TestBuildToolEvents_SkipsStartEventsMissingRequiredFields(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "", ToolName: "bash"}),
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "id", ToolName: ""}),
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "good", ToolName: "view"}),
		mkEvent(t0.Add(time.Millisecond), &copilot.ToolExecutionCompleteData{ToolCallID: "good", Success: true}),
	}
	out := buildToolEvents(events)
	require.Len(t, out, 1)
	require.Equal(t, "good", out[0].ToolCallID)
}

func TestBuildToolEvents_NilArgsAreNotEmitted(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	events := []copilot.SessionEvent{
		mkEvent(t0, &copilot.ToolExecutionStartData{ToolCallID: "x", ToolName: "bash"}),
		mkEvent(t0.Add(time.Millisecond), &copilot.ToolExecutionCompleteData{ToolCallID: "x", Success: true}),
	}
	out := buildToolEvents(events)
	require.Len(t, out, 1)
	require.Nil(t, out[0].Args)
}
