// Package agentevents defines a provider-neutral event model that waza's
// execution layer uses to describe agent runs. It is intentionally independent
// of any specific agent SDK so the rest of the codebase (orchestration,
// graders, transcripts, web API, CLI suggestion path) can consume execution
// output without importing a particular SDK's event types.
//
// Engine adapters are responsible for converting SDK-native events into the
// neutral types defined here. For the Copilot engine, see
// internal/execution/agentevents_adapter.go.
//
// Phase 1 (issue #10) scope: this package intentionally retains one narrow
// SDK reference - ToolExecutionCompleteData.Result keeps the SDK's
// *copilot.ToolExecutionCompleteResult type so that the existing JSON wire
// format (transcripts, dashboard payloads) is preserved bit-for-bit. Phase 2
// will replace this with a neutral result type.
package agentevents

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
)

// EventType identifies the kind of an Event. Constants below use the same
// string values as the underlying engine's event types so the JSON wire
// format produced for transcripts is unchanged.
type EventType string

const (
	EventTypeAssistantMessage           EventType = "assistant.message"
	EventTypeAssistantMessageDelta      EventType = "assistant.message_delta"
	EventTypeAssistantReasoning         EventType = "assistant.reasoning"
	EventTypeAssistantUsage             EventType = "assistant.usage"
	EventTypeUserMessage                EventType = "user.message"
	EventTypeSystemMessage              EventType = "system.message"
	EventTypeSessionStart               EventType = "session.start"
	EventTypeSessionIdle                EventType = "session.idle"
	EventTypeSessionError               EventType = "session.error"
	EventTypeSessionInfo                EventType = "session.info"
	EventTypeSessionWarning             EventType = "session.warning"
	EventTypeSessionShutdown            EventType = "session.shutdown"
	EventTypeSkillInvoked               EventType = "skill.invoked"
	EventTypeToolExecutionStart         EventType = "tool.execution_start"
	EventTypeToolExecutionComplete      EventType = "tool.execution_complete"
	EventTypeToolExecutionProgress      EventType = "tool.execution_progress"
	EventTypeToolExecutionPartialResult EventType = "tool.execution_partial_result"
	EventTypeToolUserRequested          EventType = "tool.user_requested"
	EventTypeHookStart                  EventType = "hook.start"
	EventTypeHookEnd                    EventType = "hook.end"
)

// EventData is the payload carried by an Event. Concrete payload types
// implement Type() to declare which EventType they represent.
type EventData interface {
	Type() EventType
}

// Event is a single neutral agent execution event.
type Event struct {
	Data EventData
}

// Type returns the EventType of e's payload. Returns "" for a zero Event.
func (e Event) Type() EventType {
	if e.Data == nil {
		return ""
	}
	return e.Data.Type()
}

// ---------- Concrete payload types ----------

type UserMessageData struct {
	Content string
}

func (*UserMessageData) Type() EventType { return EventTypeUserMessage }

type AssistantMessageData struct {
	Content       string
	ReasoningText *string
}

func (*AssistantMessageData) Type() EventType { return EventTypeAssistantMessage }

type AssistantMessageDeltaData struct {
	DeltaContent string
}

func (*AssistantMessageDeltaData) Type() EventType { return EventTypeAssistantMessageDelta }

type AssistantReasoningData struct {
	Content string
}

func (*AssistantReasoningData) Type() EventType { return EventTypeAssistantReasoning }

type SystemMessageData struct {
	Content string
}

func (*SystemMessageData) Type() EventType { return EventTypeSystemMessage }

type AssistantUsageData struct{}

func (*AssistantUsageData) Type() EventType { return EventTypeAssistantUsage }

type SessionStartData struct{}

func (*SessionStartData) Type() EventType { return EventTypeSessionStart }

type SessionIdleData struct{}

func (*SessionIdleData) Type() EventType { return EventTypeSessionIdle }

type SessionShutdownData struct{}

func (*SessionShutdownData) Type() EventType { return EventTypeSessionShutdown }

type SessionErrorData struct {
	Message string
}

func (*SessionErrorData) Type() EventType { return EventTypeSessionError }

type SessionInfoData struct {
	Message string
}

func (*SessionInfoData) Type() EventType { return EventTypeSessionInfo }

type SessionWarningData struct {
	Message string
}

func (*SessionWarningData) Type() EventType { return EventTypeSessionWarning }

type SkillInvokedData struct {
	Name string
	Path string
}

func (*SkillInvokedData) Type() EventType { return EventTypeSkillInvoked }

type ToolExecutionStartData struct {
	ToolCallID string
	ToolName   string
	Arguments  any
}

func (*ToolExecutionStartData) Type() EventType { return EventTypeToolExecutionStart }

// ToolExecutionCompleteData carries the result of a completed tool call.
//
// Phase 1 residual SDK coupling: Result intentionally keeps the SDK's
// *copilot.ToolExecutionCompleteResult type so that transcript JSON output
// and dashboard payloads remain identical to today. Replacing Result with a
// fully neutral type is tracked as Phase 2 work for issue #10.
type ToolExecutionCompleteData struct {
	ToolCallID string
	Success    bool
	Result     *copilot.ToolExecutionCompleteResult
}

func (*ToolExecutionCompleteData) Type() EventType { return EventTypeToolExecutionComplete }

type ToolExecutionPartialResultData struct {
	ToolCallID    string
	PartialOutput string
}

func (*ToolExecutionPartialResultData) Type() EventType {
	return EventTypeToolExecutionPartialResult
}

type ToolExecutionProgressData struct {
	ToolCallID string
}

func (*ToolExecutionProgressData) Type() EventType { return EventTypeToolExecutionProgress }

type ToolUserRequestedData struct {
	Arguments  any
	ToolCallID string
	ToolName   string
}

func (*ToolUserRequestedData) Type() EventType { return EventTypeToolUserRequested }

type HookStartData struct{}

func (*HookStartData) Type() EventType { return EventTypeHookStart }

type HookEndData struct{}

func (*HookEndData) Type() EventType { return EventTypeHookEnd }

// RawData is the fallback payload for event types without a dedicated
// neutral struct. EventType is preserved so Type() round-trips correctly.
type RawData struct {
	EventType EventType
	Raw       json.RawMessage
}

func (r *RawData) Type() EventType { return r.EventType }

// NewRawData builds a RawData payload by JSON-encoding arbitrary data.
// Marshaling errors fall back to an empty object so the event type still
// round-trips.
func NewRawData(eventType EventType, data any) *RawData {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	return &RawData{EventType: eventType, Raw: raw}
}

// ---------- Accessor helpers ----------
//
// These mirror the shape of internal/copilotevents helpers so call sites can
// migrate with minimal churn.

// Content returns the textual content for message-bearing events
// (user/assistant/assistant-reasoning/system messages).
func Content(e Event) (string, bool) {
	switch d := e.Data.(type) {
	case *UserMessageData:
		return d.Content, true
	case *AssistantMessageData:
		return d.Content, true
	case *AssistantReasoningData:
		return d.Content, true
	case *SystemMessageData:
		return d.Content, true
	}
	return "", false
}

// DeltaContent returns the streaming delta for assistant message deltas.
func DeltaContent(e Event) (string, bool) {
	if d, ok := e.Data.(*AssistantMessageDeltaData); ok {
		return d.DeltaContent, true
	}
	return "", false
}

// Message returns the message text for session error/info/warning events.
func Message(e Event) (string, bool) {
	switch d := e.Data.(type) {
	case *SessionErrorData:
		return d.Message, true
	case *SessionInfoData:
		return d.Message, true
	case *SessionWarningData:
		return d.Message, true
	}
	return "", false
}

// ReasoningText returns the assistant reasoning text pointer for assistant
// message events, or nil when not present.
func ReasoningText(e Event) *string {
	if d, ok := e.Data.(*AssistantMessageData); ok {
		return d.ReasoningText
	}
	return nil
}

func SessionStart(e Event) (*SessionStartData, bool) {
	d, ok := e.Data.(*SessionStartData)
	return d, ok
}

func Shutdown(e Event) (*SessionShutdownData, bool) {
	d, ok := e.Data.(*SessionShutdownData)
	return d, ok
}

func AssistantUsage(e Event) (*AssistantUsageData, bool) {
	d, ok := e.Data.(*AssistantUsageData)
	return d, ok
}

func SkillInvoked(e Event) (*SkillInvokedData, bool) {
	d, ok := e.Data.(*SkillInvokedData)
	return d, ok
}

func ToolStart(e Event) (*ToolExecutionStartData, bool) {
	d, ok := e.Data.(*ToolExecutionStartData)
	return d, ok
}

func ToolComplete(e Event) (*ToolExecutionCompleteData, bool) {
	d, ok := e.Data.(*ToolExecutionCompleteData)
	return d, ok
}

func ToolPartial(e Event) (*ToolExecutionPartialResultData, bool) {
	d, ok := e.Data.(*ToolExecutionPartialResultData)
	return d, ok
}

func ToolProgress(e Event) (*ToolExecutionProgressData, bool) {
	d, ok := e.Data.(*ToolExecutionProgressData)
	return d, ok
}

func ToolUserRequested(e Event) (*ToolUserRequestedData, bool) {
	d, ok := e.Data.(*ToolUserRequestedData)
	return d, ok
}

func HookStart(e Event) (*HookStartData, bool) {
	d, ok := e.Data.(*HookStartData)
	return d, ok
}

func HookEnd(e Event) (*HookEndData, bool) {
	d, ok := e.Data.(*HookEndData)
	return d, ok
}

// ToolCallID returns the tool-call identifier for any tool-related event
// kind. The boolean is false for events without a tool call ID.
func ToolCallID(e Event) (string, bool) {
	switch d := e.Data.(type) {
	case *ToolExecutionStartData:
		return d.ToolCallID, d.ToolCallID != ""
	case *ToolExecutionCompleteData:
		return d.ToolCallID, d.ToolCallID != ""
	case *ToolExecutionPartialResultData:
		return d.ToolCallID, d.ToolCallID != ""
	case *ToolExecutionProgressData:
		return d.ToolCallID, d.ToolCallID != ""
	case *ToolUserRequestedData:
		return d.ToolCallID, d.ToolCallID != ""
	}
	return "", false
}
