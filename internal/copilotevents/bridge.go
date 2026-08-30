package copilotevents

import (
	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/agentevent"
)

// FromSDK wraps a slice of Copilot SDK session events as engine-neutral
// [agentevent.Event] values. The underlying SDK event is preserved on each
// returned Event via Event.Raw() so consumers that still need SDK-typed data
// can unwrap with [AsSDKEvent] or [ToSDK]. Phase 2 will replace those typed
// accesses with kind-based generic accessors.
func FromSDK(events []copilot.SessionEvent) []agentevent.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]agentevent.Event, len(events))
	for i, evt := range events {
		out[i] = WrapSDKEvent(evt)
	}
	return out
}

// WrapSDKEvent wraps a single Copilot SDK session event as an engine-neutral
// [agentevent.Event].
func WrapSDKEvent(evt copilot.SessionEvent) agentevent.Event {
	return agentevent.New(kindFromSDK(evt.Type()), evt)
}

// AsSDKEvent returns the Copilot SDK session event that was wrapped into the
// given engine-neutral event. The second return value reports whether the
// event was produced by the Copilot engine (and therefore carries an SDK
// payload). Events produced by non-Copilot engines yield the zero
// [copilot.SessionEvent] and false.
func AsSDKEvent(e agentevent.Event) (copilot.SessionEvent, bool) {
	se, ok := e.Raw().(copilot.SessionEvent)
	return se, ok
}

// ToSDK extracts the underlying Copilot SDK events from a slice of
// engine-neutral events. Events that were not produced by the Copilot engine
// are skipped — callers that need to distinguish them should iterate with
// [AsSDKEvent] instead.
func ToSDK(events []agentevent.Event) []copilot.SessionEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]copilot.SessionEvent, 0, len(events))
	for _, e := range events {
		if se, ok := AsSDKEvent(e); ok {
			out = append(out, se)
		}
	}
	return out
}

// kindFromSDK translates an SDK event type into the engine-neutral [agentevent.Kind].
// Unknown SDK event types fall through to [agentevent.KindRaw].
func kindFromSDK(t copilot.SessionEventType) agentevent.Kind {
	switch t {
	case copilot.SessionEventTypeSessionStart:
		return agentevent.KindSessionStart
	case copilot.SessionEventTypeSessionShutdown:
		return agentevent.KindSessionShutdown
	case copilot.SessionEventTypeSessionError:
		return agentevent.KindSessionError
	case copilot.SessionEventTypeSessionInfo:
		return agentevent.KindSessionInfo
	case copilot.SessionEventTypeSessionWarning:
		return agentevent.KindSessionWarning
	case copilot.SessionEventTypeUserMessage:
		return agentevent.KindUserMessage
	case copilot.SessionEventTypeAssistantMessage:
		return agentevent.KindAssistantMessage
	case copilot.SessionEventTypeAssistantMessageDelta:
		return agentevent.KindAssistantMessageDelta
	case copilot.SessionEventTypeAssistantReasoning:
		return agentevent.KindAssistantReasoning
	case copilot.SessionEventTypeAssistantUsage:
		return agentevent.KindAssistantUsage
	case copilot.SessionEventTypeSystemMessage:
		return agentevent.KindSystemMessage
	case copilot.SessionEventTypeToolExecutionStart:
		return agentevent.KindToolExecutionStart
	case copilot.SessionEventTypeToolExecutionComplete:
		return agentevent.KindToolExecutionComplete
	case copilot.SessionEventTypeToolExecutionPartialResult:
		return agentevent.KindToolExecutionPartialResult
	case copilot.SessionEventTypeToolExecutionProgress:
		return agentevent.KindToolExecutionProgress
	case copilot.SessionEventTypeToolUserRequested:
		return agentevent.KindToolUserRequested
	case copilot.SessionEventTypeSkillInvoked:
		return agentevent.KindSkillInvoked
	case copilot.SessionEventTypeHookStart:
		return agentevent.KindHookStart
	case copilot.SessionEventTypeHookEnd:
		return agentevent.KindHookEnd
	default:
		return agentevent.KindRaw
	}
}
