package models

import (
	"encoding/json"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestTranscriptEventRoundTrip(t *testing.T) {
	content := "hello world"
	message := "some message"
	toolCallID := "call-123"
	toolName := "bash"
	success := true

	original := TranscriptEvent{
		SessionEvent: copilot.SessionEvent{
			Type: copilot.ToolExecutionComplete,
			Data: copilot.Data{
				Content:    &content,
				Message:    &message,
				ToolCallID: &toolCallID,
				ToolName:   &toolName,
				Arguments:  map[string]any{"command": "ls"},
				Result:     &copilot.Result{Content: "file1.go"},
				Success:    &success,
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

	if restored.Type != original.Type {
		t.Errorf("Type: got %v, want %v", restored.Type, original.Type)
	}
	assertStringPtr(t, "Content", restored.Data.Content, original.Data.Content)
	assertStringPtr(t, "Message", restored.Data.Message, original.Data.Message)
	assertStringPtr(t, "ToolCallID", restored.Data.ToolCallID, original.Data.ToolCallID)
	assertStringPtr(t, "ToolName", restored.Data.ToolName, original.Data.ToolName)
	assertBoolPtr(t, "Success", restored.Data.Success, original.Data.Success)

	if restored.Data.Result == nil {
		t.Fatal("Result is nil after round-trip")
	}
	if restored.Data.Result.Content != original.Data.Result.Content {
		t.Errorf("Result.Content: got %q, want %q", restored.Data.Result.Content, original.Data.Result.Content)
	}

	// Arguments round-trips as map[string]any via JSON
	argsMap, ok := restored.Data.Arguments.(map[string]any)
	if !ok {
		t.Fatalf("Arguments: expected map[string]any, got %T", restored.Data.Arguments)
	}
	if argsMap["command"] != "ls" {
		t.Errorf("Arguments[command]: got %v, want %q", argsMap["command"], "ls")
	}
}

func TestTranscriptEventUnmarshalMinimal(t *testing.T) {
	input := `{"type":"tool.execution_start"}`

	var te TranscriptEvent
	if err := json.Unmarshal([]byte(input), &te); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	if te.Type != copilot.ToolExecutionStart {
		t.Errorf("Type: got %v, want %v", te.Type, copilot.ToolExecutionStart)
	}
	if te.Data.Content != nil {
		t.Errorf("Content should be nil, got %v", te.Data.Content)
	}
}

func assertStringPtr(t *testing.T, name string, got, want *string) {
	t.Helper()
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil {
		t.Errorf("%s: got %v, want %v", name, got, want)
		return
	}
	if *got != *want {
		t.Errorf("%s: got %q, want %q", name, *got, *want)
	}
}

func assertBoolPtr(t *testing.T, name string, got, want *bool) {
	t.Helper()
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil {
		t.Errorf("%s: got %v, want %v", name, got, want)
		return
	}
	if *got != *want {
		t.Errorf("%s: got %v, want %v", name, *got, *want)
	}
}
