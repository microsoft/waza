package copilotevents

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
)

func Content(event copilot.SessionEvent) (string, bool) {
	switch data := event.Data.(type) {
	case *copilot.UserMessageData:
		return data.Content, true
	case *copilot.AssistantMessageData:
		return data.Content, true
	case *copilot.AssistantReasoningData:
		return data.Content, true
	case *copilot.SystemMessageData:
		return data.Content, true
	default:
		return "", false
	}
}

func DeltaContent(event copilot.SessionEvent) (string, bool) {
	if data, ok := event.Data.(*copilot.AssistantMessageDeltaData); ok {
		return data.DeltaContent, true
	}
	return "", false
}

func Message(event copilot.SessionEvent) (string, bool) {
	switch data := event.Data.(type) {
	case *copilot.SessionErrorData:
		return data.Message, true
	case *copilot.SessionInfoData:
		return data.Message, true
	case *copilot.SessionWarningData:
		return data.Message, true
	default:
		return "", false
	}
}

func ReasoningText(event copilot.SessionEvent) *string {
	if data, ok := event.Data.(*copilot.AssistantMessageData); ok {
		return data.ReasoningText
	}
	return nil
}

func SessionStart(event copilot.SessionEvent) (*copilot.SessionStartData, bool) {
	data, ok := event.Data.(*copilot.SessionStartData)
	return data, ok
}

func ToolStart(event copilot.SessionEvent) (*copilot.ToolExecutionStartData, bool) {
	data, ok := event.Data.(*copilot.ToolExecutionStartData)
	return data, ok
}

func ToolUserRequested(event copilot.SessionEvent) (*copilot.ToolUserRequestedData, bool) {
	data, ok := event.Data.(*copilot.ToolUserRequestedData)
	return data, ok
}

func ToolComplete(event copilot.SessionEvent) (*copilot.ToolExecutionCompleteData, bool) {
	data, ok := event.Data.(*copilot.ToolExecutionCompleteData)
	return data, ok
}

func ToolPartial(event copilot.SessionEvent) (*copilot.ToolExecutionPartialResultData, bool) {
	data, ok := event.Data.(*copilot.ToolExecutionPartialResultData)
	return data, ok
}

func ToolProgress(event copilot.SessionEvent) (*copilot.ToolExecutionProgressData, bool) {
	data, ok := event.Data.(*copilot.ToolExecutionProgressData)
	return data, ok
}

func ToolCallID(event copilot.SessionEvent) (string, bool) {
	switch data := event.Data.(type) {
	case *copilot.ToolExecutionStartData:
		return data.ToolCallID, data.ToolCallID != ""
	case *copilot.ToolExecutionCompleteData:
		return data.ToolCallID, data.ToolCallID != ""
	case *copilot.ToolExecutionPartialResultData:
		return data.ToolCallID, data.ToolCallID != ""
	case *copilot.ToolExecutionProgressData:
		return data.ToolCallID, data.ToolCallID != ""
	case *copilot.ToolUserRequestedData:
		return data.ToolCallID, data.ToolCallID != ""
	default:
		return "", false
	}
}

func SkillInvoked(event copilot.SessionEvent) (*copilot.SkillInvokedData, bool) {
	data, ok := event.Data.(*copilot.SkillInvokedData)
	return data, ok
}

func HookStart(event copilot.SessionEvent) (*copilot.HookStartData, bool) {
	data, ok := event.Data.(*copilot.HookStartData)
	return data, ok
}

func HookEnd(event copilot.SessionEvent) (*copilot.HookEndData, bool) {
	data, ok := event.Data.(*copilot.HookEndData)
	return data, ok
}

func Shutdown(event copilot.SessionEvent) (*copilot.SessionShutdownData, bool) {
	data, ok := event.Data.(*copilot.SessionShutdownData)
	return data, ok
}

func AssistantUsage(event copilot.SessionEvent) (*copilot.AssistantUsageData, bool) {
	data, ok := event.Data.(*copilot.AssistantUsageData)
	return data, ok
}

func RawData(eventType copilot.SessionEventType, data any) copilot.SessionEventData {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	return &copilot.RawSessionEventData{EventType: eventType, Raw: raw}
}
