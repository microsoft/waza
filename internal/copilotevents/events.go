package copilotevents

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
)

const legacyToolExecutionPartialResultType copilot.SessionEventType = "tool.execution_partial_result"

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

func ToolShellOutput(event copilot.SessionEvent) (*copilot.ToolShellOutputData, bool) {
	data, ok := event.Data.(*copilot.ToolShellOutputData)
	return data, ok
}

// ToolOutput reads current streaming shell output and legacy partial-result
// payloads emitted by the embedded CLI before shell output streaming was added.
func ToolOutput(event copilot.SessionEvent) (text, toolCallID string, ok bool) {
	if output, ok := ToolShellOutput(event); ok {
		return output.Text, output.ToolCallID, true
	}
	if event.Type() != legacyToolExecutionPartialResultType {
		return "", "", false
	}

	var raw []byte
	switch data := event.Data.(type) {
	case *copilot.RawSessionEventData:
		raw = data.Raw
	default:
		var err error
		raw, err = json.Marshal(data)
		if err != nil {
			return "", "", false
		}
	}

	var legacy struct {
		PartialOutput string `json:"partialOutput"`
		ToolCallID    string `json:"toolCallId"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return "", "", false
	}
	return legacy.PartialOutput, legacy.ToolCallID, true
}

func ToolProgress(event copilot.SessionEvent) (*copilot.ToolExecutionProgressData, bool) {
	data, ok := event.Data.(*copilot.ToolExecutionProgressData)
	return data, ok
}

func ToolCallID(event copilot.SessionEvent) (string, bool) {
	if _, toolCallID, ok := ToolOutput(event); ok {
		return toolCallID, toolCallID != ""
	}

	switch data := event.Data.(type) {
	case *copilot.ToolExecutionStartData:
		return data.ToolCallID, data.ToolCallID != ""
	case *copilot.ToolExecutionCompleteData:
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
