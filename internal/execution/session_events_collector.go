package execution

import (
	"fmt"
	"os"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

const sessionFailedUnknown = "session failed with unknown error"

type SessionEventsCollector struct {
	// SkillInvocations is a chronological list of skills invoked during the session
	SkillInvocations []SkillInvocation

	sessionEvents  []copilot.SessionEvent
	outputParts    []string
	errorMsg       string
	done           chan struct{}
	intentToolIDs  map[string]bool
	onSkillInvoked func(SkillInvocation) // optional callback fired on each SkillInvoked event
}

// NewSessionEventsCollector creates a new SessionEvents.
func NewSessionEventsCollector() *SessionEventsCollector {
	return &SessionEventsCollector{
		done:          make(chan struct{}),
		intentToolIDs: map[string]bool{},
	}
}

// SessionEvents returns the collected session events.
func (coll *SessionEventsCollector) SessionEvents() []copilot.SessionEvent {
	return coll.sessionEvents
}

// OutputParts returns the collected output text parts.
func (coll *SessionEventsCollector) OutputParts() []string {
	return coll.outputParts
}

// ErrorMessage returns the error message, if any.
func (coll *SessionEventsCollector) ErrorMessage() string {
	return coll.errorMsg
}

// Done returns the channel that is closed when the session completes.
func (coll *SessionEventsCollector) Done() <-chan struct{} {
	return coll.done
}

// SetOnSkillInvoked registers a callback that fires every time a SkillInvoked
// event is received. The callback runs synchronously inside On(), so it can
// safely cancel a context to abort an in-flight SendAndWait.
func (coll *SessionEventsCollector) SetOnSkillInvoked(fn func(SkillInvocation)) {
	coll.onSkillInvoked = fn
}

// On is a callback, intended to be passed to [copilot.Session.On] to receive
// events in real-time.
func (coll *SessionEventsCollector) On(event copilot.SessionEvent) {
	switch event.Type() {
	case copilot.SessionEventTypeAssistantMessage:
		if d, ok := event.Data.(*copilot.AssistantMessageData); ok {
			coll.outputParts = append(coll.outputParts, d.Content)
		}
	case copilot.SessionEventTypeAssistantMessageDelta:
		if d, ok := event.Data.(*copilot.AssistantMessageDeltaData); ok {
			coll.outputParts = append(coll.outputParts, d.DeltaContent)
		}
	case copilot.SessionEventTypeSkillInvoked:
		if d, ok := event.Data.(*copilot.SkillInvokedData); ok {
			si := SkillInvocation{Name: d.Name, Path: d.Path}
			if si.Name != "" || si.Path != "" {
				coll.SkillInvocations = append(coll.SkillInvocations, si)
				if coll.onSkillInvoked != nil {
					coll.onSkillInvoked(si)
				}
			} else {
				// this shouldn't happen but if it does we at least want to know about it
				if _, err := fmt.Fprintf(os.Stderr, "warning: received SkillInvoked event with no Name or Path: %+v\n", event); err != nil {
					// this also shouldn't happen but if it does something's very wrong
					panic("failed to write to stderr: " + err.Error())
				}
			}
		}
	case copilot.SessionEventTypeToolExecutionStart:
		if d, ok := event.Data.(*copilot.ToolExecutionStartData); ok && d.ToolName == "report_intent" {
			// report_intent always seems to be followed by the actual tool invocation,
			// so I'm just going to skip these to save a little space.
			coll.intentToolIDs[d.ToolCallID] = true
			return
		}
	case copilot.SessionEventTypeToolExecutionProgress,
		copilot.SessionEventTypeToolUserRequested:
		if toolCallID, ok := toolCallIDFromEventData(event.Data); ok && coll.intentToolIDs[toolCallID] {
			return
		}
	case copilot.SessionEventTypeToolExecutionComplete,
		copilot.SessionEventTypeToolExecutionPartialResult:
		if toolCallID, ok := toolCallIDFromEventData(event.Data); ok && coll.intentToolIDs[toolCallID] {
			delete(coll.intentToolIDs, toolCallID)
			return
		}
	// these are both termination events
	case copilot.SessionEventTypeSessionIdle, copilot.SessionEventTypeSessionError:
		if d, ok := event.Data.(*copilot.SessionErrorData); ok {
			if d.Message == "" {
				coll.errorMsg = sessionFailedUnknown
			} else {
				coll.errorMsg = d.Message
			}
		}

		select {
		case <-coll.done:
		default:
			close(coll.done)
		}
	}

	coll.sessionEvents = append(coll.sessionEvents, event)
}

func toolCallIDFromEventData(data copilot.SessionEventData) (string, bool) {
	switch d := data.(type) {
	case *copilot.ToolExecutionStartData:
		return d.ToolCallID, true
	case *copilot.ToolExecutionProgressData:
		return d.ToolCallID, true
	case *copilot.ToolUserRequestedData:
		return d.ToolCallID, true
	case *copilot.ToolExecutionCompleteData:
		return d.ToolCallID, true
	case *copilot.ToolExecutionPartialResultData:
		return d.ToolCallID, true
	default:
		return "", false
	}
}

// ToolCalls goes through the list of session events and correlates tool starts
// with Success. The resulting tool calls are not cached - if you're going to use
// it repeatedly you should store it locally.
func (coll *SessionEventsCollector) ToolCalls() []models.ToolCall {
	return models.FilterToolCalls(coll.sessionEvents)
}
