package models

import (
	"encoding/json"
	"fmt"
	"log/slog"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/copilotevents"
)

// ToolCall represents a tool invocation
type ToolCall struct {
	// ID is the engine-assigned identifier for this call (e.g. the
	// Copilot SDK's ToolCallID). Used to correlate tool invocations in
	// observability backends.
	ID        string                               `json:"id,omitempty"`
	Name      string                               `json:"name"`
	Arguments ToolCallArgs                         `json:"arguments,omitempty"`
	Result    *copilot.ToolExecutionCompleteResult `json:"result,omitempty"`
	Success   bool                                 `json:"success"`
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

	// Extra captures any tool-specific argument keys that aren't part of
	// the fixed fields above (e.g. `query`/`limit` for search tools,
	// MCP-specific payloads). Populated automatically by mapstructure's
	// ",remain" support so argument matchers in tool_calls /
	// tool_constraint graders can see the full argument bag.
	//
	// Serialized inline alongside the fixed fields via [ToolCallArgs.MarshalJSON]
	// / [ToolCallArgs.UnmarshalJSON] so that MCP-style keys (e.g. `query`)
	// survive a round-trip through `results.json` and are visible to graders
	// during offline `waza grade` runs. Known-field keys always win — a
	// collision (unusual, since mapstructure only populates Extra with keys
	// it could not place) is silently dropped from Extra during unmarshal.
	Extra map[string]any `json:"-" mapstructure:",remain"`
}

// toolCallArgsKnownFields lists the JSON keys owned by ToolCallArgs's fixed
// struct fields. Kept as a package-level var so MarshalJSON and UnmarshalJSON
// share a single source of truth and can quickly discriminate Extra keys.
var toolCallArgsKnownFields = map[string]struct{}{
	"path":        {},
	"file_text":   {},
	"command":     {},
	"description": {},
	"skill":       {},
}

// MarshalJSON emits the fixed fields (preserving the historical shape —
// every known key is always present, even when the value is the zero
// string) and inlines any [ToolCallArgs.Extra] entries at the same level.
//
// Inlining Extra fixes issue #474: previously `Extra` had `json:"-"`, so
// MCP tool arguments such as `query` and `limit` were dropped when
// `session_digest.tool_calls[].arguments` was written to `results.json`,
// which made the `tool_calls` grader's `expect[].args` matcher fail during
// offline grading (`waza grade`) even though the live run had matched.
//
// This change is additive to the wire format: existing consumers still see
// the same known keys with the same values; new keys are simply also
// present when the engine supplied them.
func (a ToolCallArgs) MarshalJSON() ([]byte, error) {
	// Alias suppresses the receiver's custom MarshalJSON to reuse the
	// default reflection-based marshaller for the fixed fields.
	type alias ToolCallArgs
	base, err := json.Marshal(alias(a))
	if err != nil {
		return nil, err
	}
	if len(a.Extra) == 0 {
		return base, nil
	}

	// Decode the known-field object, splice Extra in, re-encode. This is
	// simpler than string manipulation and keeps encoding/json responsible
	// for escaping and ordering.
	merged := make(map[string]json.RawMessage, len(toolCallArgsKnownFields)+len(a.Extra))
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range a.Extra {
		if _, isKnown := toolCallArgsKnownFields[k]; isKnown {
			// Should not happen under normal mapstructure usage, but if
			// it does, defer to the known field's value.
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshaling ToolCallArgs.Extra[%q]: %w", k, err)
		}
		merged[k] = raw
	}
	return json.Marshal(merged)
}

// UnmarshalJSON reads the fixed fields and captures every other top-level
// key into [ToolCallArgs.Extra]. This is the read side of the inlining
// contract documented on MarshalJSON: it lets `waza grade` reconstitute
// full MCP argument bags from a serialized `session_digest.tool_calls[]`.
func (a *ToolCallArgs) UnmarshalJSON(data []byte) error {
	// Empty-object / null are common in older captures; treat them as a
	// zero value rather than an error.
	if len(data) == 0 || string(data) == "null" {
		*a = ToolCallArgs{}
		return nil
	}

	// Reuse the reflection-based unmarshaller for the known fields.
	type alias ToolCallArgs
	var fixed alias
	if err := json.Unmarshal(data, &fixed); err != nil {
		return err
	}

	// Second pass: collect anything that isn't a known field into Extra.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var extra map[string]any
	for k, v := range raw {
		if _, isKnown := toolCallArgsKnownFields[k]; isKnown {
			continue
		}
		var decoded any
		if err := json.Unmarshal(v, &decoded); err != nil {
			return fmt.Errorf("unmarshaling ToolCallArgs.Extra[%q]: %w", k, err)
		}
		if extra == nil {
			extra = make(map[string]any, len(raw))
		}
		extra[k] = decoded
	}

	*a = ToolCallArgs(fixed)
	a.Extra = extra
	return nil
}

type TranscriptEvent struct {
	copilot.SessionEvent `json:"-"`
}

func (te TranscriptEvent) MarshalJSON() ([]byte, error) {
	v := struct {
		Content *string                  `json:"content,omitempty"`
		Type    copilot.SessionEventType `json:"type"`

		Message *string `json:"message,omitempty"`

		// tool call fields
		Arguments  any                                  `json:"arguments,omitempty"`
		Success    *bool                                `json:"success,omitempty"`
		ToolCallID *string                              `json:"tool_call_id,omitempty"`
		ToolName   *string                              `json:"tool_name,omitempty"`
		ToolResult *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
	}{
		Type: te.Type(),
	}

	if content, ok := copilotevents.Content(te.SessionEvent); ok {
		v.Content = &content
	}
	if message, ok := copilotevents.Message(te.SessionEvent); ok {
		v.Message = &message
	}
	if start, ok := copilotevents.ToolStart(te.SessionEvent); ok {
		v.ToolCallID = &start.ToolCallID
		v.ToolName = &start.ToolName
		v.Arguments = start.Arguments
	}
	if complete, ok := copilotevents.ToolComplete(te.SessionEvent); ok {
		v.ToolCallID = &complete.ToolCallID
		v.ToolResult = complete.Result
		v.Success = &complete.Success
	}
	if partial, ok := copilotevents.ToolPartial(te.SessionEvent); ok {
		v.ToolCallID = &partial.ToolCallID
	}

	return json.Marshal(v)
}

func (te *TranscriptEvent) UnmarshalJSON(data []byte) error {
	var v struct {
		Content    *string                              `json:"content,omitempty"`
		Type       copilot.SessionEventType             `json:"type"`
		Message    *string                              `json:"message,omitempty"`
		Arguments  any                                  `json:"arguments,omitempty"`
		Success    *bool                                `json:"success,omitempty"`
		ToolCallID *string                              `json:"tool_call_id,omitempty"`
		ToolName   *string                              `json:"tool_name,omitempty"`
		ToolResult *copilot.ToolExecutionCompleteResult `json:"tool_result,omitempty"`
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	te.Data = transcriptData(v.Type, v.Content, v.Message, v.ToolCallID, v.ToolName, v.Arguments, v.ToolResult, v.Success)

	return nil
}

func transcriptData(
	eventType copilot.SessionEventType,
	content *string,
	message *string,
	toolCallID *string,
	toolName *string,
	arguments any,
	toolResult *copilot.ToolExecutionCompleteResult,
	success *bool,
) copilot.SessionEventData {
	switch eventType {
	case copilot.SessionEventTypeUserMessage:
		return &copilot.UserMessageData{Content: derefString(content)}
	case copilot.SessionEventTypeAssistantMessage:
		return &copilot.AssistantMessageData{Content: derefString(content)}
	case copilot.SessionEventTypeAssistantMessageDelta:
		return &copilot.AssistantMessageDeltaData{DeltaContent: derefString(content)}
	case copilot.SessionEventTypeToolExecutionStart:
		return &copilot.ToolExecutionStartData{
			Arguments:  arguments,
			ToolCallID: derefString(toolCallID),
			ToolName:   derefString(toolName),
		}
	case copilot.SessionEventTypeToolExecutionComplete:
		return &copilot.ToolExecutionCompleteData{
			Result:     toolResult,
			Success:    derefBool(success),
			ToolCallID: derefString(toolCallID),
		}
	case copilot.SessionEventTypeToolExecutionPartialResult:
		return &copilot.ToolExecutionPartialResultData{ToolCallID: derefString(toolCallID)}
	case copilot.SessionEventTypeSessionError:
		return &copilot.SessionErrorData{Message: derefString(message)}
	default:
		return copilotevents.RawData(eventType, map[string]any{
			"content":      content,
			"message":      message,
			"arguments":    arguments,
			"success":      success,
			"tool_call_id": toolCallID,
			"tool_name":    toolName,
			"tool_result":  toolResult,
		})
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefBool(value *bool) bool {
	return value != nil && *value
}

// FilterToolCalls goes through the list of session events and correlates tool starts
// with Success.
func FilterToolCalls(sessionEvents []copilot.SessionEvent) []ToolCall {
	toolCallsMap := map[string]*ToolCall{}
	var toolCallIDs []string // preserve the start order of the events.

	for _, evt := range sessionEvents {
		switch evt.Type() {
		case copilot.SessionEventTypeToolExecutionStart:
			start, ok := copilotevents.ToolStart(evt)
			if !ok || start.ToolName == "" || start.ToolCallID == "" {
				continue
			}

			tc := &ToolCall{
				ID:   start.ToolCallID,
				Name: start.ToolName,
			}

			if err := mapstructure.Decode(start.Arguments, &tc.Arguments); err != nil {
				slog.Warn("tool argument format wasn't recognized", "error", err, "name", start.ToolName, "args", start.Arguments)
			}

			toolCallsMap[start.ToolCallID] = tc
			toolCallIDs = append(toolCallIDs, start.ToolCallID)
		case copilot.SessionEventTypeToolExecutionComplete:
			complete, ok := copilotevents.ToolComplete(evt)
			if !ok || complete.ToolCallID == "" {
				continue
			}
			tc := toolCallsMap[complete.ToolCallID]
			if tc == nil {
				continue
			}

			tc.Success = complete.Success
			tc.Result = complete.Result
		}
	}

	var toolCalls []ToolCall

	for _, id := range toolCallIDs {
		toolCalls = append(toolCalls, *toolCallsMap[id])
	}

	return toolCalls
}
