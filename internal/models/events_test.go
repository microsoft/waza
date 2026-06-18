package models

import (
	"encoding/json"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func TestTranscriptEventRoundTrip(t *testing.T) {
	content := "hello world"
	message := "some message"
	toolCallID := "call-123"
	toolName := "bash"
	success := true

	original := TranscriptEvent{
		SessionEvent: copilot.SessionEvent{
			Data: &copilot.ToolExecutionCompleteData{
				ToolCallID: toolCallID,
				Result: &copilot.ToolExecutionCompleteResult{
					Content: "file1.go",
				},
				Success: success,
			},
		},
	}
	_ = content
	_ = message
	_ = toolName

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
	restoredData, ok := restored.Data.(*copilot.ToolExecutionCompleteData)
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
	if te.Type() != copilot.SessionEventTypeToolExecutionStart {
		t.Errorf("Type: got %v, want %v", te.Type(), copilot.SessionEventTypeToolExecutionStart)
	}
	_, ok := te.Data.(*copilot.ToolExecutionStartData)
	require.True(t, ok)
}
