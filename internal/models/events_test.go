package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranscriptEventRoundTrip(t *testing.T) {
	toolCallID := "call-123"
	success := true

	original := TranscriptEvent{
		SessionEvent: SessionEvent{
			EventType:  SessionEventTypeToolExecutionComplete,
			ToolCallID: &toolCallID,
			ToolResult: &ToolExecutionResult{
				Content: "file1.go",
			},
			Success: &success,
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored TranscriptEvent
	require.NoError(t, json.Unmarshal(data, &restored))

	require.Equal(t, original.Type(), restored.Type())
	require.NotNil(t, restored.ToolCallID)
	require.Equal(t, toolCallID, *restored.ToolCallID)
	require.NotNil(t, restored.Success)
	require.Equal(t, success, *restored.Success)
	require.NotNil(t, restored.ToolResult)
	require.Equal(t, "file1.go", restored.ToolResult.Content)
}

func TestTranscriptEventUnmarshalMinimal(t *testing.T) {
	input := `{"type":"tool.execution_start"}`

	var te TranscriptEvent
	require.NoError(t, json.Unmarshal([]byte(input), &te))
	require.Equal(t, SessionEventTypeToolExecutionStart, te.Type())
}

// TestTranscriptEventRoundTripPreservesUncoveredType guards against losing the
// event type for kinds without dedicated payload fields (for example
// session.idle, session.shutdown, assistant.usage).
func TestTranscriptEventRoundTripPreservesUncoveredType(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventType SessionEventType
	}{
		{"idle", SessionEventTypeSessionIdle},
		{"shutdown", SessionEventTypeSessionShutdown},
		{"assistant_usage", SessionEventTypeAssistantUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := TranscriptEvent{SessionEvent: SessionEvent{EventType: tc.eventType}}
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
