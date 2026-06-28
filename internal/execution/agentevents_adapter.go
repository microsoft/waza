package execution

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/agentevents"
)

// FromCopilotEvent converts a Copilot SDK session event into the neutral
// agentevents.Event used throughout waza. Unknown event types fall through
// to agentevents.RawData so the kind round-trips.
func FromCopilotEvent(e copilot.SessionEvent) agentevents.Event {
	switch d := e.Data.(type) {
	case *copilot.UserMessageData:
		return agentevents.Event{Data: &agentevents.UserMessageData{Content: d.Content}}
	case *copilot.AssistantMessageData:
		return agentevents.Event{Data: &agentevents.AssistantMessageData{
			Content:       d.Content,
			ReasoningText: d.ReasoningText,
		}}
	case *copilot.AssistantMessageDeltaData:
		return agentevents.Event{Data: &agentevents.AssistantMessageDeltaData{DeltaContent: d.DeltaContent}}
	case *copilot.AssistantReasoningData:
		return agentevents.Event{Data: &agentevents.AssistantReasoningData{Content: d.Content}}
	case *copilot.SystemMessageData:
		return agentevents.Event{Data: &agentevents.SystemMessageData{Content: d.Content}}
	case *copilot.AssistantUsageData:
		return agentevents.Event{Data: &agentevents.AssistantUsageData{}}
	case *copilot.SessionStartData:
		return agentevents.Event{Data: &agentevents.SessionStartData{}}
	case *copilot.SessionIdleData:
		return agentevents.Event{Data: &agentevents.SessionIdleData{}}
	case *copilot.SessionShutdownData:
		return agentevents.Event{Data: &agentevents.SessionShutdownData{}}
	case *copilot.SessionErrorData:
		return agentevents.Event{Data: &agentevents.SessionErrorData{Message: d.Message}}
	case *copilot.SessionInfoData:
		return agentevents.Event{Data: &agentevents.SessionInfoData{Message: d.Message}}
	case *copilot.SessionWarningData:
		return agentevents.Event{Data: &agentevents.SessionWarningData{Message: d.Message}}
	case *copilot.SkillInvokedData:
		return agentevents.Event{Data: &agentevents.SkillInvokedData{Name: d.Name, Path: d.Path}}
	case *copilot.ToolExecutionStartData:
		return agentevents.Event{Data: &agentevents.ToolExecutionStartData{
			ToolCallID: d.ToolCallID,
			ToolName:   d.ToolName,
			Arguments:  d.Arguments,
		}}
	case *copilot.ToolExecutionCompleteData:
		return agentevents.Event{Data: &agentevents.ToolExecutionCompleteData{
			ToolCallID: d.ToolCallID,
			Success:    d.Success,
			Result:     d.Result,
		}}
	case *copilot.ToolExecutionPartialResultData:
		return agentevents.Event{Data: &agentevents.ToolExecutionPartialResultData{
			ToolCallID:    d.ToolCallID,
			PartialOutput: d.PartialOutput,
		}}
	case *copilot.ToolExecutionProgressData:
		return agentevents.Event{Data: &agentevents.ToolExecutionProgressData{ToolCallID: d.ToolCallID}}
	case *copilot.ToolUserRequestedData:
		return agentevents.Event{Data: &agentevents.ToolUserRequestedData{Arguments: d.Arguments, ToolCallID: d.ToolCallID, ToolName: d.ToolName}}
	case *copilot.HookStartData:
		return agentevents.Event{Data: &agentevents.HookStartData{}}
	case *copilot.HookEndData:
		return agentevents.Event{Data: &agentevents.HookEndData{}}
	case *copilot.RawSessionEventData:
		return agentevents.Event{Data: &agentevents.RawData{
			EventType: agentevents.EventType(d.EventType),
			Raw:       d.Raw,
		}}
	}
	// Unrecognized SDK data type: best-effort preserve the kind string and
	// JSON form so consumers can still see it.
	raw, err := json.Marshal(e.Data)
	if err != nil {
		raw = []byte("{}")
	}
	return agentevents.Event{Data: &agentevents.RawData{
		EventType: agentevents.EventType(e.Type()),
		Raw:       raw,
	}}
}
