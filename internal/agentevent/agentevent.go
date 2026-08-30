// Package agentevent defines engine-neutral session event types that decouple
// ExecutionResponse and downstream consumers from any single agent SDK.
//
// An [Event] carries a [Kind] (an engine-neutral classifier) together with an
// opaque per-engine payload accessible via [Event.Raw]. Engines convert their
// native event stream into Events at the boundary; consumers that still need
// engine-specific data unwrap [Event.Raw] via an engine-specific helper (for
// example internal/copilotevents.AsSDKEvent).
//
// This package is deliberately minimal: Phase 1 of issue #10 introduces the
// abstraction seam without forcing every consumer to migrate to generic
// accessors. Phase 2 will replace remaining type-asserted access to the
// underlying SDK payload with kind-based generic accessors so additional
// engines (Claude Code, Codex, generic CLI) can plug in without leaking
// engine-specific types into the rest of the codebase.
package agentevent

// Kind classifies a session event independently of the underlying agent SDK.
// Values mirror the lifecycle stages emitted by typical agent runtimes
// (session lifecycle, user/assistant turns, tool execution, skill invocation,
// hooks). New engines may introduce additional kinds — consumers should treat
// unknown kinds as opaque and fall through to KindRaw-style handling.
type Kind string

const (
	// KindUnknown is the zero value. Events that have not been classified
	// fall back to this so callers can distinguish "not set" from a known
	// kind.
	KindUnknown Kind = ""

	KindSessionStart    Kind = "session_start"
	KindSessionShutdown Kind = "session_shutdown"
	KindSessionError    Kind = "session_error"
	KindSessionInfo     Kind = "session_info"
	KindSessionWarning  Kind = "session_warning"

	KindUserMessage           Kind = "user_message"
	KindAssistantMessage      Kind = "assistant_message"
	KindAssistantMessageDelta Kind = "assistant_message_delta"
	KindAssistantReasoning    Kind = "assistant_reasoning"
	KindAssistantUsage        Kind = "assistant_usage"
	KindSystemMessage         Kind = "system_message"

	KindToolExecutionStart         Kind = "tool_execution_start"
	KindToolExecutionComplete      Kind = "tool_execution_complete"
	KindToolExecutionPartialResult Kind = "tool_execution_partial_result"
	KindToolExecutionProgress      Kind = "tool_execution_progress"
	KindToolUserRequested          Kind = "tool_user_requested"

	KindSkillInvoked Kind = "skill_invoked"
	KindHookStart    Kind = "hook_start"
	KindHookEnd      Kind = "hook_end"

	// KindRaw is the fallback for engine-specific events that do not
	// correspond to any of the kinds above. The full payload is reachable
	// via Event.Raw().
	KindRaw Kind = "raw"
)

// Event is an engine-neutral session event. It pairs an engine-neutral [Kind]
// with an opaque payload supplied by the producing engine. Construction is
// reserved for engine adapters via [New]; consumers should treat Event as a
// read-only value.
type Event struct {
	kind Kind
	raw  any
}

// New constructs an Event with the given kind and engine-specific payload.
// The payload may be nil. Engine adapters call this once per emitted event;
// downstream consumers should never need to construct Events directly.
func New(kind Kind, raw any) Event {
	return Event{kind: kind, raw: raw}
}

// Kind returns the engine-neutral classifier for this event.
func (e Event) Kind() Kind { return e.kind }

// Raw returns the engine-specific payload attached to this event, or nil if
// the producing engine did not attach one. Callers must type-assert through
// an engine-specific helper (e.g. internal/copilotevents.AsSDKEvent) rather
// than depending on a particular concrete type here.
func (e Event) Raw() any { return e.raw }

// TextProvider is an optional interface that engine-specific event payloads
// (returned from [Event.Raw]) may implement to expose the human-readable text
// content of an event without forcing consumers to type-assert against a
// concrete SDK type.
//
// Engine adapters are encouraged to implement this on payloads that carry
// natural-language content (assistant messages, user messages, reasoning,
// system messages). Consumers such as ExecutionResponse.ExtractMessages use
// it as an engine-neutral fallback when an event was not produced by the
// Copilot SDK.
type TextProvider interface {
	// Text returns the text content of the event and true. Implementations
	// that have no text for a given event should return ("", false) rather
	// than an empty string, so callers can distinguish "no content" from
	// "empty content".
	Text() (string, bool)
}

// Text returns the human-readable text content of the event, if its
// engine-specific payload implements [TextProvider]. Returns ("", false) when
// the payload is nil or does not implement the interface.
func (e Event) Text() (string, bool) {
	if e.raw == nil {
		return "", false
	}
	if tp, ok := e.raw.(TextProvider); ok {
		return tp.Text()
	}
	return "", false
}
