package models

import (
	"encoding/json"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/agentevents"
	"github.com/stretchr/testify/require"
)

func TestTranscriptEventRoundTrip(t *testing.T) {
	toolCallID := "call-123"
	success := true

	original := TranscriptEvent{
		Event: agentevents.Event{
			Data: &agentevents.ToolExecutionCompleteData{
				ToolCallID: toolCallID,
				Result: &copilot.ToolExecutionCompleteResult{
					Content: "file1.go",
				},
				Success: success,
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	var restored TranscriptEvent
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if restored.Type() != original.Type() {
		t.Errorf("Type: got %v, want %v", restored.Type(), original.Type())
	}
	restoredData, ok := restored.Data.(*agentevents.ToolExecutionCompleteData)
	require.True(t, ok)
	require.Equal(t, toolCallID, restoredData.ToolCallID)
	require.Equal(t, success, restoredData.Success)
	if restoredData.Result == nil {
		t.Fatal("Result is nil after round-trip")
	}

	require.Equal(t, "file1.go", restoredData.Result.Content)
}

func TestTranscriptEventUnmarshalMinimal(t *testing.T) {
	input := `{"type":"tool.execution_start"}`

	var te TranscriptEvent
	if err := json.Unmarshal([]byte(input), &te); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if te.Type() != agentevents.EventTypeToolExecutionStart {
		t.Errorf("Type: got %v, want %v", te.Type(), agentevents.EventTypeToolExecutionStart)
	}
	_, ok := te.Data.(*agentevents.ToolExecutionStartData)
	require.True(t, ok)
}

// TestTranscriptEventRoundTripPreservesUncoveredType guards against losing the
// event type for kinds without a dedicated transcriptData mapping (for example
// session.idle, session.shutdown, assistant.usage). These fall to the
// RawData default case, which only reports the right Type() if its
// EventType field is set. mapTranscriptEvents in the web API relies on Type().
func TestTranscriptEventRoundTripPreservesUncoveredType(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType agentevents.EventType
		data      agentevents.EventData
	}{
		{"idle", agentevents.EventTypeSessionIdle, &agentevents.SessionIdleData{}},
		{"shutdown", agentevents.EventTypeSessionShutdown, &agentevents.SessionShutdownData{}},
		{"assistant_usage", agentevents.EventTypeAssistantUsage, &agentevents.AssistantUsageData{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := TranscriptEvent{Event: agentevents.Event{Data: tc.data}}
			require.Equal(t, tc.eventType, original.Type())

			data, err := json.Marshal(original)
			require.NoError(t, err)

			var restored TranscriptEvent
			require.NoError(t, json.Unmarshal(data, &restored))
			require.Equal(t, tc.eventType, restored.Type(),
				"event type must round-trip through marshal/unmarshal")
		})
	}
}
