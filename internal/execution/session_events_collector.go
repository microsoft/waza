package execution

import (
	"fmt"
	"os"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
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
	firstEvent     chan struct{}
	firstEventOnce sync.Once
	intentToolIDs  map[string]bool
	onSkillInvoked func(SkillInvocation) // optional callback fired on each SkillInvoked event
}

// NewSessionEventsCollector creates a new SessionEvents.
func NewSessionEventsCollector() *SessionEventsCollector {
	return &SessionEventsCollector{
		done:          make(chan struct{}),
		firstEvent:    make(chan struct{}),
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

// FirstEvent returns a channel that is closed when the FIRST session event of
// any type is received. A healthy session emits events promptly once the agent
// starts its first turn; a session-start hang (the embedded engine launches but
// never produces a turn) emits nothing at all. Callers use this to arm a
// short "time to first event" deadline distinct from the overall turn timeout,
// so a no-first-turn wedge is caught in seconds instead of running out the full
// (necessarily large) turn budget.
func (coll *SessionEventsCollector) FirstEvent() <-chan struct{} {
	return coll.firstEvent
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
	// Signal first contact before anything else: ANY event proves the engine is
	// alive and has begun the turn, which disarms the first-event watchdog. We
	// deliberately fire on the first event of any type (not only assistant/tool
	// events) and err toward never false-aborting a slow-but-live first turn —
	// a true session-start hang emits no events at all.
	coll.firstEventOnce.Do(func() { close(coll.firstEvent) })

	switch event.Type() {
	case copilot.SessionEventTypeAssistantMessage:
		if content, ok := copilotevents.Content(event); ok {
			coll.outputParts = append(coll.outputParts, content)
		}

	case copilot.SessionEventTypeSkillInvoked:
		si := SkillInvocation{}
		// these and Content (the text of the relevant SKILL.md) are the only consistently populated fields
		if data, ok := copilotevents.SkillInvoked(event); ok {
			si.Name = data.Name
			si.Path = data.Path
		}
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

	case copilot.SessionEventTypeToolExecutionStart:
		if data, ok := copilotevents.ToolStart(event); ok && data.ToolName == "report_intent" {
			// report_intent always seems to be followed by the actual tool invocation,
			// so I'm just going to skip these to save a little space.
			if data.ToolCallID != "" {
				coll.intentToolIDs[data.ToolCallID] = true
			}
			return
		}
	case copilot.SessionEventTypeToolExecutionProgress,
		copilot.SessionEventTypeToolUserRequested:
		if toolCallID, ok := copilotevents.ToolCallID(event); ok && coll.intentToolIDs[toolCallID] {
			return
		}

	case copilot.SessionEventTypeToolExecutionComplete, copilot.SessionEventTypeToolExecutionPartialResult:
		if toolCallID, ok := copilotevents.ToolCallID(event); ok && coll.intentToolIDs[toolCallID] {
			delete(coll.intentToolIDs, toolCallID)
			return
		}
	// these are both termination events
	case copilot.SessionEventTypeSessionIdle, copilot.SessionEventTypeSessionError:
		if event.Type() == copilot.SessionEventTypeSessionError {
			if message, ok := copilotevents.Message(event); !ok || message == "" {
				coll.errorMsg = sessionFailedUnknown
			} else {
				coll.errorMsg = message
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

// ToolCalls goes through the list of session events and correlates tool starts
// with Success. The resulting tool calls are not cached - if you're going to use
// it repeatedly you should store it locally.
func (coll *SessionEventsCollector) ToolCalls() []models.ToolCall {
	return models.FilterToolCalls(coll.sessionEvents)
}
