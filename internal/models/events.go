package models

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
)

// ToolCall represents a tool invocation
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments any             `json:"arguments,omitempty"`
	Result    *copilot.Result `json:"result,omitempty"`
	Success   bool            `json:"success"`
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
		Arguments  any             `json:"arguments,omitempty"`
		Success    *bool           `json:"success,omitempty"`
		ToolCallID *string         `json:"tool_call_id,omitempty"`
		ToolName   *string         `json:"tool_name,omitempty"`
		ToolResult *copilot.Result `json:"tool_result,omitempty"`
	}{
		Type: te.Type,

		// response messages
		Content: te.Data.Content,
		Message: te.Data.Message,

		// tool call related fields
		ToolCallID: te.Data.ToolCallID,
		ToolName:   te.Data.ToolName,
		Arguments:  te.Data.Arguments,
		ToolResult: te.Data.Result,
		Success:    te.Data.Success,
	}

	return json.Marshal(v)
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Content    *string                  `json:"content,omitempty"`
		Type       copilot.SessionEventType `json:"type"`
		Message    *string                  `json:"message,omitempty"`
		Arguments  any                      `json:"arguments,omitempty"`
		Success    *bool                    `json:"success,omitempty"`
		ToolCallID *string                  `json:"tool_call_id,omitempty"`
		ToolName   *string                  `json:"tool_name,omitempty"`
		ToolResult *copilot.Result          `json:"tool_result,omitempty"`
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	te.Type = v.Type
	te.Data.Content = v.Content
	te.Data.Message = v.Message
	te.Data.ToolCallID = v.ToolCallID
	te.Data.ToolName = v.ToolName
	te.Data.Arguments = v.Arguments
	te.Data.Result = v.ToolResult
	te.Data.Success = v.Success

	return nil
}

// FilterToolCalls goes through the list of session events and correlates tool starts
// with Success.
func FilterToolCalls(sessionEvents []copilot.SessionEvent) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch evt.Type {
		case copilot.ToolExecutionStart:
			if evt.Data.ToolName == nil || evt.Data.ToolCallID == nil {
				continue
			}
			tc := &ToolCall{
				Name:      *evt.Data.ToolName,
				Arguments: evt.Data.Arguments,
			}
			toolCallsMap[*evt.Data.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, *evt.Data.ToolCallID)
		case copilot.ToolExecutionComplete, copilot.ToolExecutionPartialResult:
			if evt.Data.ToolCallID == nil {
				continue
			}
			tc := toolCallsMap[*evt.Data.ToolCallID]
			if tc == nil {
				continue
			}

			if evt.Data.Success != nil {
				tc.Success = *evt.Data.Success
			}

			tc.Result = evt.Data.Result
		}
	}

	var toolCalls []ToolCall

	for _, id := range toolCallIDs {
		toolCalls = append(toolCalls, *toolCallsMap[id])
	}

	return toolCalls
}
