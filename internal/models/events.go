package models

import (
	"encoding/json"
	"log/slog"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/agentevents"
)

// ToolCall represents a tool invocation.
//
// Phase 1 residual SDK coupling (issue #10): Result intentionally keeps the
// SDK type so transcript and dashboard JSON output remains unchanged. A
// neutral result type is tracked as Phase 2 work.
type ToolCall struct {
	// ID is the engine-assigned identifier for this call (e.g. the
	// Copilot SDK's ToolCallID). Used to correlate tool invocations in
	// observability backends.
	ID        string                               `json:"id,omitempty"`
	Name      string                               `json:"name"`
	Arguments ToolCallArgs                         `json:"arguments,omitempty"`
	Result    *copilot.ToolExecutionCompleteResult `json:"result,omitempty"`
	Success   bool                                 `json:"success"`
}

type ToolCallArgs struct {
	// these are filled out for file-based tools (view/edit)
	Path     string `json:"path"      mapstructure:"path"`
	FileText string `json:"file_text" mapstructure:"file_text"`

	// filled out for tools like bash or powershell
	Command     string `json:"command"     mapstructure:"command"`
	Description string `json:"description" mapstructure:"description"`

	// filled out for skill invocations
	Skill string `json:"skill" mapstructure:"skill"`
}

// TranscriptEvent wraps a neutral agent event with custom JSON marshaling that
// preserves the transcript wire format used by dashboards and CI consumers.
//
// The embedded field is named `Event` so call sites construct events with
// `TranscriptEvent{Event: ev}` and reach the underlying type via `te.Type()`
// and `te.Data` via field promotion.
type TranscriptEvent struct {
	agentevents.Event `json:"-"`
}

func (te TranscriptEvent) MarshalJSON() ([]byte, error) {
	v := struct {
		Content *string               `json:"content,omitempty"`
		Type    agentevents.EventType `json:"type"`

		Message *string `json:"message,omitempty"`

		// tool call fields
		Arguments  any                                  `json:"arguments,omitempty"`
		Success    *bool                                `json:"success,omitempty"`
		ToolCallID *string                              `json:"tool_call_id,omitempty"`
		ToolName   *string                              `json:"tool_name,omitempty"`
		ToolResult *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
	}{
		Type: te.Type(),
	}

	if content, ok := agentevents.Content(te.Event); ok {
		v.Content = &content
	}
	if message, ok := agentevents.Message(te.Event); ok {
		v.Message = &message
	}
	if start, ok := agentevents.ToolStart(te.Event); ok {
		v.ToolCallID = &start.ToolCallID
		v.ToolName = &start.ToolName
		v.Arguments = start.Arguments
	}
	if complete, ok := agentevents.ToolComplete(te.Event); ok {
		v.ToolCallID = &complete.ToolCallID
		v.ToolResult = complete.Result
		v.Success = &complete.Success
	}
	if partial, ok := agentevents.ToolPartial(te.Event); ok {
		v.ToolCallID = &partial.ToolCallID
	}

	return json.Marshal(v)
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Content    *string                              `json:"content,omitempty"`
		Type       agentevents.EventType                `json:"type"`
		Message    *string                              `json:"message,omitempty"`
		Arguments  any                                  `json:"arguments,omitempty"`
		Success    *bool                                `json:"success,omitempty"`
		ToolCallID *string                              `json:"tool_call_id,omitempty"`
		ToolName   *string                              `json:"tool_name,omitempty"`
		ToolResult *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	te.Event = agentevents.Event{
		Data: transcriptData(v.Type, v.Content, v.Message, v.ToolCallID, v.ToolName, v.Arguments, v.ToolResult, v.Success),
	}

	return nil
}

func transcriptData(
	eventType agentevents.EventType,
	content *string,
	message *string,
	toolCallID *string,
	toolName *string,
	arguments any,
	toolResult *copilot.ToolExecutionCompleteResult,
	success *bool,
) agentevents.EventData {
	switch eventType {
	case agentevents.EventTypeUserMessage:
		return &agentevents.UserMessageData{Content: derefString(content)}
	case agentevents.EventTypeAssistantMessage:
		return &agentevents.AssistantMessageData{Content: derefString(content)}
	case agentevents.EventTypeAssistantMessageDelta:
		return &agentevents.AssistantMessageDeltaData{DeltaContent: derefString(content)}
	case agentevents.EventTypeToolExecutionStart:
		return &agentevents.ToolExecutionStartData{
			Arguments:  arguments,
			ToolCallID: derefString(toolCallID),
			ToolName:   derefString(toolName),
		}
	case agentevents.EventTypeToolExecutionComplete:
		return &agentevents.ToolExecutionCompleteData{
			Result:     toolResult,
			Success:    derefBool(success),
			ToolCallID: derefString(toolCallID),
		}
	case agentevents.EventTypeToolExecutionPartialResult:
		return &agentevents.ToolExecutionPartialResultData{ToolCallID: derefString(toolCallID)}
	case agentevents.EventTypeSessionError:
		return &agentevents.SessionErrorData{Message: derefString(message)}
	default:
		return agentevents.NewRawData(eventType, map[string]any{
			"content":      content,
			"message":      message,
			"arguments":    arguments,
			"success":      success,
			"tool_call_id": toolCallID,
			"tool_name":    toolName,
			"tool_result":  toolResult,
		})
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefBool(value *bool) bool {
	return value != nil && *value
}

// FilterToolCalls goes through the list of session events and correlates tool
// starts with their completion Success/Result.
func FilterToolCalls(sessionEvents []agentevents.Event) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch evt.Type() {
		case agentevents.EventTypeToolExecutionStart:
			start, ok := agentevents.ToolStart(evt)
			if !ok || start.ToolName == "" || start.ToolCallID == "" {
				continue
			}

			tc := &ToolCall{
				ID:   start.ToolCallID,
				Name: start.ToolName,
			}

			if err := mapstructure.Decode(start.Arguments, &tc.Arguments); err != nil {
				slog.Warn("tool argument format wasn't recognized", "error", err, "name", start.ToolName, "args", start.Arguments)
			}

			toolCallsMap[start.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, start.ToolCallID)
		case agentevents.EventTypeToolExecutionComplete:
			complete, ok := agentevents.ToolComplete(evt)
			if !ok || complete.ToolCallID == "" {
				continue
			}
			tc := toolCallsMap[complete.ToolCallID]
			if tc == nil {
				continue
			}

			tc.Success = complete.Success
			tc.Result = complete.Result
		}
	}

	var toolCalls []ToolCall

	for _, id := range toolCallIDs {
		toolCalls = append(toolCalls, *toolCallsMap[id])
	}

	return toolCalls
}
