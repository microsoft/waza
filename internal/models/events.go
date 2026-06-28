package models

import (
	"encoding/json"
	"log/slog"

	"github.com/go-viper/mapstructure/v2"
)

// SessionEventType identifies an engine event in a provider-neutral form.
type SessionEventType string

const (
	SessionEventTypeUserMessage                SessionEventType = "user.message"
	SessionEventTypeSystemMessage              SessionEventType = "system.message"
	SessionEventTypeAssistantMessage           SessionEventType = "assistant.message"
	SessionEventTypeAssistantMessageDelta      SessionEventType = "assistant.message_delta"
	SessionEventTypeAssistantReasoning         SessionEventType = "assistant.reasoning"
	SessionEventTypeAssistantTurnStart         SessionEventType = "assistant.turn_start"
	SessionEventTypeAssistantTurnEnd           SessionEventType = "assistant.turn_end"
	SessionEventTypeAssistantUsage             SessionEventType = "assistant.usage"
	SessionEventTypeToolExecutionStart         SessionEventType = "tool.execution_start"
	SessionEventTypeToolExecutionComplete      SessionEventType = "tool.execution_complete"
	SessionEventTypeToolExecutionPartialResult SessionEventType = "tool.execution_partial_result"
	SessionEventTypeToolExecutionProgress      SessionEventType = "tool.execution_progress"
	SessionEventTypeToolUserRequested          SessionEventType = "tool.user_requested"
	SessionEventTypeSkillInvoked               SessionEventType = "skill.invoked"
	SessionEventTypeSessionStart               SessionEventType = "session.start"
	SessionEventTypeSessionIdle                SessionEventType = "session.idle"
	SessionEventTypeSessionError               SessionEventType = "session.error"
	SessionEventTypeSessionInfo                SessionEventType = "session.info"
	SessionEventTypeSessionUsageInfo           SessionEventType = "session.usage_info"
	SessionEventTypeSessionWarning             SessionEventType = "session.warning"
	SessionEventTypeSessionShutdown            SessionEventType = "session.shutdown"
	SessionEventTypePendingMessagesModified    SessionEventType = "pending_messages.modified"
	SessionEventTypeHookStart                  SessionEventType = "hook.start"
	SessionEventTypeHookEnd                    SessionEventType = "hook.end"
)

// ToolExecutionResult is the provider-neutral representation of a tool result.
type ToolExecutionResult struct {
	Content         string  `json:"content,omitempty"`
	DetailedContent *string `json:"detailedContent,omitempty"`
}

// ToolCall represents a tool invocation.
type ToolCall struct {
	// ID is the engine-assigned identifier for this call (e.g. the
	// Copilot SDK's ToolCallID). Used to correlate tool invocations in
	// observability backends.
	ID        string               `json:"id,omitempty"`
	Name      string               `json:"name"`
	Arguments ToolCallArgs         `json:"arguments,omitempty"`
	Result    *ToolExecutionResult `json:"result,omitempty"`
	Success   bool                 `json:"success"`
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

// SessionEvent is the provider-neutral event model exposed by execution engines.
type SessionEvent struct {
	EventType SessionEventType `json:"type"`

	Content      *string `json:"content,omitempty"`
	DeltaContent *string `json:"-"`
	Message      *string `json:"message,omitempty"`

	// tool call fields
	Arguments     any                  `json:"arguments,omitempty"`
	Success       *bool                `json:"success,omitempty"`
	ToolCallID    *string              `json:"tool_call_id,omitempty"`
	ToolName      *string              `json:"tool_name,omitempty"`
	ToolResult    *ToolExecutionResult `json:"tool_result,omitempty"`
	PartialOutput *string              `json:"-"`

	// skill invocation fields are kept in-memory for behavior, but are omitted
	// from transcript JSON to preserve the existing output contract.
	SkillName *string `json:"-"`
	SkillPath *string `json:"-"`
}

// Type returns the event kind while preserving the old call pattern used by
// transcript and test code.
func (e SessionEvent) Type() SessionEventType {
	return e.EventType
}

type TranscriptEvent struct {
	SessionEvent `json:"-"`
}

func (te TranscriptEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(transcriptEventJSON{
		Content:    te.Content,
		Type:       te.Type(),
		Message:    te.Message,
		Arguments:  te.Arguments,
		Success:    te.Success,
		ToolCallID: te.ToolCallID,
		ToolName:   te.ToolName,
		ToolResult: te.ToolResult,
	})
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v transcriptEventJSON
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	te.SessionEvent = SessionEvent{
		EventType:  v.Type,
		Content:    v.Content,
		Message:    v.Message,
		Arguments:  v.Arguments,
		Success:    v.Success,
		ToolCallID: v.ToolCallID,
		ToolName:   v.ToolName,
		ToolResult: v.ToolResult,
	}

	return nil
}

type transcriptEventJSON struct {
	Content *string          `json:"content,omitempty"`
	Type    SessionEventType `json:"type"`

	Message *string `json:"message,omitempty"`

	// tool call fields
	Arguments  any                  `json:"arguments,omitempty"`
	Success    *bool                `json:"success,omitempty"`
	ToolCallID *string              `json:"tool_call_id,omitempty"`
	ToolName   *string              `json:"tool_name,omitempty"`
	ToolResult *ToolExecutionResult `json:"tool_result,omitempty"`
}

// FilterToolCalls goes through the list of session events and correlates tool starts
// with Success.
func FilterToolCalls(sessionEvents []SessionEvent) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch evt.Type() {
		case SessionEventTypeToolExecutionStart:
			if evt.ToolName == nil || *evt.ToolName == "" || evt.ToolCallID == nil || *evt.ToolCallID == "" {
				continue
			}

			tc := &ToolCall{
				ID:   *evt.ToolCallID,
				Name: *evt.ToolName,
			}

			if err := mapstructure.Decode(evt.Arguments, &tc.Arguments); err != nil {
				slog.Warn("tool argument format wasn't recognized", "error", err, "name", *evt.ToolName, "args", evt.Arguments)
			}

			toolCallsMap[*evt.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, *evt.ToolCallID)
		case SessionEventTypeToolExecutionComplete:
			if evt.ToolCallID == nil || *evt.ToolCallID == "" {
				continue
			}
			tc := toolCallsMap[*evt.ToolCallID]
			if tc == nil {
				continue
			}

			if evt.Success != nil {
				tc.Success = *evt.Success
			}
			tc.Result = evt.ToolResult
		}
	}

	var toolCalls []ToolCall

	for _, id := range toolCallIDs {
		toolCalls = append(toolCalls, *toolCallsMap[id])
	}

	return toolCalls
}
