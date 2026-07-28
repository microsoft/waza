package models

import "encoding/json"

// ToolEvent is the normalized, engine-agnostic record of a single tool call
// emitted during an agent session. It is the canonical per-task tool record
// surfaced in `results.json` under `runs[].tool_events` (schema version 1.1+).
//
// Fields are intentionally aligned with the GenAI OpenTelemetry semantic
// conventions (see internal/telemetry) so the same record can be exported as
// a span attribute set without translation:
//
//	tool_call_id   <-> gen_ai.tool.call.id
//	tool_name      <-> gen_ai.tool.name
//	args           <-> gen_ai.tool.arguments (JSON value)
//	result         <-> gen_ai.tool.result    (JSON value)
//	success        <-> waza.tool.success
//
// Wave 3 (#367) snapshot/replay consumes tool_events directly, so the wire
// format must remain stable: additions are MINOR (1.x), renames/removals are
// MAJOR (2.0).
type ToolEvent struct {
	// Turn is the 1-based ordinal of the assistant turn that produced this
	// call. Multiple tool calls may share the same turn. 0 means unknown
	// (engines that do not surface turn boundaries).
	Turn int `json:"turn"`

	// Sequence is the 1-based ordinal of this call within the run, in the
	// order the engine emitted them. Stable across replays.
	Sequence int `json:"sequence"`

	// ToolCallID is the engine's unique identifier for this call. May be
	// empty for engines that do not assign IDs.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolName is the canonical name of the tool that was invoked.
	ToolName string `json:"tool_name"`

	// Args is the call's argument payload normalised to a JSON-compatible
	// value (object, array, scalar, or null). The exact shape depends on
	// the tool; argmatcher matchers in tool_calls / tool_constraint graders
	// run against this field.
	Args any `json:"args,omitempty"`

	// Result is the tool's response payload normalised to a JSON-compatible
	// value, or nil when the engine did not surface a result.
	Result any `json:"result,omitempty"`

	// Success reports whether the engine considered the call to have
	// completed without error.
	Success bool `json:"success"`

	// Error is the engine's failure message when Success is false. Empty
	// for successful calls.
	Error string `json:"error,omitempty"`

	// DurationMs is the wall-clock time between the tool-start and
	// tool-complete events when available. 0 when timing is unavailable.
	DurationMs int64 `json:"duration_ms,omitempty"`
}

// SessionDigestWithToolEvents returns a copy of digest whose ToolCalls are
// hydrated from canonical ToolEvents when they are available. The legacy
// SessionDigest.ToolCalls field intentionally stays in results.json for
// backward compatibility, but ToolCallArgs.Extra is not serialized there; this
// adapter lets graders use the replay-friendly tool_events[].args payload
// without changing the public schema.
func SessionDigestWithToolEvents(digest SessionDigest, events []ToolEvent) SessionDigest {
	if len(events) == 0 {
		return digest
	}

	calls := toolCallsFromToolEvents(digest.ToolCalls, events)
	digest.ToolCalls = calls
	digest.ToolCallCount = len(calls)
	digest.ToolsUsed = make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Name == "" {
			continue
		}
		digest.ToolsUsed = append(digest.ToolsUsed, call.Name)
	}
	return digest
}

func toolCallsFromToolEvents(fallback []ToolCall, events []ToolEvent) []ToolCall {
	byID := make(map[string]ToolCall, len(fallback))
	for _, call := range fallback {
		if call.ID != "" {
			byID[call.ID] = call
		}
	}

	calls := make([]ToolCall, 0, len(events))
	for i, event := range events {
		call := ToolCall{}
		if event.ToolCallID != "" {
			call = byID[event.ToolCallID]
		}
		if call.Name == "" && i < len(fallback) {
			call = fallback[i]
		}

		if event.ToolCallID != "" {
			call.ID = event.ToolCallID
		}
		if event.ToolName != "" {
			call.Name = event.ToolName
		}
		if event.Args != nil {
			call.Arguments = toolCallArgsFromEventArgs(event.Args)
		}
		call.Success = event.Success

		if call.Name == "" {
			continue
		}
		calls = append(calls, call)
	}
	return calls
}

func toolCallArgsFromEventArgs(args any) ToolCallArgs {
	raw, ok := jsonObject(args)
	if !ok {
		return ToolCallArgs{}
	}

	out := ToolCallArgs{
		Path:        stringArg(raw, "path"),
		FileText:    stringArg(raw, "file_text"),
		Command:     stringArg(raw, "command"),
		Description: stringArg(raw, "description"),
		Skill:       stringArg(raw, "skill"),
		Extra:       make(map[string]any, len(raw)),
	}
	for key, value := range raw {
		out.Extra[key] = value
	}
	if len(out.Extra) == 0 {
		out.Extra = nil
	}
	return out
}

func jsonObject(value any) (map[string]any, bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	return raw, true
}

func stringArg(args map[string]any, key string) string {
	value, ok := args[key].(string)
	if !ok {
		return ""
	}
	return value
}
