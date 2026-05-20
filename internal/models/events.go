package models

import (
	"encoding/json"
	"fmt"
	"log/slog"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
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

type TranscriptEventData struct {
	Content    *string                              `json:"content,omitempty"`
	Message    *string                              `json:"message,omitempty"`
	Arguments  any                                  `json:"arguments,omitempty"`
	Success    *bool                                `json:"success,omitempty"`
	ToolCallID *string                              `json:"tool_call_id,omitempty"`
	ToolName   *string                              `json:"tool_name,omitempty"`
	Result     *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
}

type TranscriptEvent struct {
	Type copilot.SessionEventType `json:"type"`
	Data TranscriptEventData      `json:"-"`
}

func (te TranscriptEvent) MarshalJSON() ([]byte, error) {
	v := struct {
		Type       copilot.SessionEventType             `json:"type"`
		Content    *string                              `json:"content,omitempty"`
		Message    *string                              `json:"message,omitempty"`
		Arguments  any                                  `json:"arguments,omitempty"`
		Success    *bool                                `json:"success,omitempty"`
		ToolCallID *string                              `json:"tool_call_id,omitempty"`
		ToolName   *string                              `json:"tool_name,omitempty"`
		ToolResult *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
	}{
		Type:       te.Type,
		Content:    te.Data.Content,
		Message:    te.Data.Message,
		Arguments:  te.Data.Arguments,
		Success:    te.Data.Success,
		ToolCallID: te.Data.ToolCallID,
		ToolName:   te.Data.ToolName,
		ToolResult: te.Data.Result,
	}

	return json.Marshal(v)
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Type       copilot.SessionEventType             `json:"type"`
		Content    *string                              `json:"content,omitempty"`
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

	te.Type = v.Type
	te.Data = TranscriptEventData{
		Content:    v.Content,
		Message:    v.Message,
		Arguments:  v.Arguments,
		Success:    v.Success,
		ToolCallID: v.ToolCallID,
		ToolName:   v.ToolName,
		Result:     v.ToolResult,
	}

	return nil
}

func NewTranscriptEvent(event copilot.SessionEvent) TranscriptEvent {
	entry := TranscriptEvent{Type: event.Type()}

	setContent := func(v string) {
		entry.Data.Content = &v
	}
	setMessage := func(v string) {
		entry.Data.Message = &v
	}
	setToolCallID := func(v string) {
		entry.Data.ToolCallID = &v
	}
	setToolName := func(v string) {
		entry.Data.ToolName = &v
	}
	setSuccess := func(v bool) {
		entry.Data.Success = &v
	}

	switch d := event.Data.(type) {
	case *copilot.UserMessageData:
		setContent(d.Content)
	case *copilot.AssistantMessageData:
		setContent(d.Content)
	case *copilot.AssistantMessageDeltaData:
		setContent(d.DeltaContent)
	case *copilot.SessionErrorData:
		setMessage(d.Message)
	case *copilot.SkillInvokedData:
		setMessage(fmt.Sprintf("%s (%s)", d.Name, d.Path))
	case *copilot.ToolExecutionStartData:
		setToolCallID(d.ToolCallID)
		setToolName(d.ToolName)
		entry.Data.Arguments = d.Arguments
	case *copilot.ToolUserRequestedData:
		setToolCallID(d.ToolCallID)
		setToolName(d.ToolName)
		entry.Data.Arguments = d.Arguments
	case *copilot.ToolExecutionProgressData:
		setToolCallID(d.ToolCallID)
		setMessage(d.ProgressMessage)
	case *copilot.ToolExecutionCompleteData:
		setToolCallID(d.ToolCallID)
		setSuccess(d.Success)
		entry.Data.Result = d.Result
		if d.Error != nil {
			setMessage(d.Error.Message)
		}
	case *copilot.ToolExecutionPartialResultData:
		setToolCallID(d.ToolCallID)
		setMessage(d.PartialOutput)
	}

	return entry
}

// FilterToolCalls goes through the list of session events and correlates tool starts
// with Success.
func FilterToolCalls(sessionEvents []copilot.SessionEvent) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch d := evt.Data.(type) {
		case *copilot.ToolExecutionStartData:
			if d.ToolName == "" || d.ToolCallID == "" {
				continue
			}

			tc := &ToolCall{Name: d.ToolName}
			if err := mapstructure.Decode(d.Arguments, &tc.Arguments); err != nil {
				slog.Warn("tool argument format wasn't recognized", "error", err, "name", d.ToolName, "args", d.Arguments)
			}

			toolCallsMap[d.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, d.ToolCallID)
		case *copilot.ToolExecutionCompleteData:
			if d.ToolCallID == "" {
				continue
			}
			tc := toolCallsMap[d.ToolCallID]
			if tc == nil {
				continue
			}

			tc.Success = d.Success
			tc.Result = d.Result
		}
	}

	var toolCalls []ToolCall
	for _, id := range toolCallIDs {
		toolCalls = append(toolCalls, *toolCallsMap[id])
	}

	return toolCalls
}
