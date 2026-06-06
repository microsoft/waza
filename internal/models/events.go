package models

import (
	"encoding/json"
	"log/slog"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/copilotevents"
)

// ToolCall represents a tool invocation
type ToolCall struct {
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

type TranscriptEvent struct {
	copilot.SessionEvent `json:"-"`
}

func (te TranscriptEvent) MarshalJSON() ([]byte, error) {
	v := struct {
		Content *string                  `json:"content,omitempty"`
		Type    copilot.SessionEventType `json:"type"`

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

	if content, ok := copilotevents.Content(te.SessionEvent); ok {
		v.Content = &content
	}
	if message, ok := copilotevents.Message(te.SessionEvent); ok {
		v.Message = &message
	}
	if start, ok := copilotevents.ToolStart(te.SessionEvent); ok {
		v.ToolCallID = &start.ToolCallID
		v.ToolName = &start.ToolName
		v.Arguments = start.Arguments
	}
	if complete, ok := copilotevents.ToolComplete(te.SessionEvent); ok {
		v.ToolCallID = &complete.ToolCallID
		v.ToolResult = complete.Result
		v.Success = &complete.Success
	}
	if partial, ok := copilotevents.ToolPartial(te.SessionEvent); ok {
		v.ToolCallID = &partial.ToolCallID
	}

	return json.Marshal(v)
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Content    *string                              `json:"content,omitempty"`
		Type       copilot.SessionEventType             `json:"type"`
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

	te.Data = transcriptData(v.Type, v.Content, v.Message, v.ToolCallID, v.ToolName, v.Arguments, v.ToolResult, v.Success)

	return nil
}

func transcriptData(
	eventType copilot.SessionEventType,
	content *string,
	message *string,
	toolCallID *string,
	toolName *string,
	arguments any,
	toolResult *copilot.ToolExecutionCompleteResult,
	success *bool,
) copilot.SessionEventData {
	switch eventType {
	case copilot.SessionEventTypeUserMessage:
		return &copilot.UserMessageData{Content: derefString(content)}
	case copilot.SessionEventTypeAssistantMessage:
		return &copilot.AssistantMessageData{Content: derefString(content)}
	case copilot.SessionEventTypeAssistantMessageDelta:
		return &copilot.AssistantMessageDeltaData{DeltaContent: derefString(content)}
	case copilot.SessionEventTypeToolExecutionStart:
		return &copilot.ToolExecutionStartData{
			Arguments:  arguments,
			ToolCallID: derefString(toolCallID),
			ToolName:   derefString(toolName),
		}
	case copilot.SessionEventTypeToolExecutionComplete:
		return &copilot.ToolExecutionCompleteData{
			Result:     toolResult,
			Success:    derefBool(success),
			ToolCallID: derefString(toolCallID),
		}
	case copilot.SessionEventTypeToolExecutionPartialResult:
		return &copilot.ToolExecutionPartialResultData{ToolCallID: derefString(toolCallID)}
	case copilot.SessionEventTypeSessionError:
		return &copilot.SessionErrorData{Message: derefString(message)}
	default:
		return copilotevents.RawData(eventType, map[string]any{
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

// FilterToolCalls goes through the list of session events and correlates tool starts
// with Success.
func FilterToolCalls(sessionEvents []copilot.SessionEvent) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch evt.Type() {
		case copilot.SessionEventTypeToolExecutionStart:
			start, ok := copilotevents.ToolStart(evt)
			if !ok || start.ToolName == "" || start.ToolCallID == "" {
				continue
			}

			tc := &ToolCall{
				Name: start.ToolName,
			}

			if err := mapstructure.Decode(start.Arguments, &tc.Arguments); err != nil {
				slog.Warn("tool argument format wasn't recognized", "error", err, "name", start.ToolName, "args", start.Arguments)
			}

			toolCallsMap[start.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, start.ToolCallID)
		case copilot.SessionEventTypeToolExecutionComplete:
			complete, ok := copilotevents.ToolComplete(evt)
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
