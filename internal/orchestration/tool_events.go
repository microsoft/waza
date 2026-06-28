package orchestration

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
)

// buildToolEvents derives the normalised []ToolEvent record from raw SDK
// session events. It is a pure function of `events` so Wave 3 snapshot/replay
// can reconstruct identical tool_events from a saved transcript.
//
// Event correlation:
//   - tool.execution.start defines the call (name, arguments, turn).
//   - tool.execution.complete attaches result, success, duration_ms.
//
// Turn numbering increments at each assistant.message event (1-based).
// SDK events that arrive before the first assistant turn are tagged turn=1.
// Calls that never received a matching complete event are still emitted with
// Success=false and DurationMs=0.
func buildToolEvents(events []copilot.SessionEvent) []models.ToolEvent {
	if len(events) == 0 {
		return nil
	}

	type pending struct {
		event models.ToolEvent
		start copilot.SessionEvent
	}
	pendingByID := make(map[string]*pending)
	orderedIDs := make([]string, 0, len(events))
	turn := 1
	seenAssistant := false

	for _, evt := range events {
		switch evt.Type() {
		case copilot.SessionEventTypeAssistantMessage:
			if seenAssistant {
				turn++
			}
			seenAssistant = true
		case copilot.SessionEventTypeToolExecutionStart:
			start, ok := copilotevents.ToolStart(evt)
			if !ok || start.ToolCallID == "" || start.ToolName == "" {
				continue
			}
			args := normalizeArgsValue(start.Arguments)
			te := models.ToolEvent{
				Turn:       turn,
				ToolCallID: start.ToolCallID,
				ToolName:   start.ToolName,
				Args:       args,
			}
			pendingByID[start.ToolCallID] = &pending{event: te, start: evt}
			orderedIDs = append(orderedIDs, start.ToolCallID)
		case copilot.SessionEventTypeToolExecutionComplete:
			complete, ok := copilotevents.ToolComplete(evt)
			if !ok || complete.ToolCallID == "" {
				continue
			}
			p, ok := pendingByID[complete.ToolCallID]
			if !ok {
				continue
			}
			p.event.Success = complete.Success
			if complete.Result != nil {
				p.event.Result = normalizeResultValue(complete.Result)
			}
			if !p.event.Success {
				// Include the result string as the error message when
				// available so consumers don't have to dig into Result.
				if s := stringifyResult(complete.Result); s != "" {
					p.event.Error = s
				}
			}
			if dur := evt.Timestamp.Sub(p.start.Timestamp).Milliseconds(); dur > 0 {
				p.event.DurationMs = dur
			}
		}
	}

	out := make([]models.ToolEvent, 0, len(orderedIDs))
	for i, id := range orderedIDs {
		p := pendingByID[id]
		p.event.Sequence = i + 1
		out = append(out, p.event)
	}
	return out
}

// normalizeArgsValue converts an arbitrary engine-provided argument payload
// into a JSON-friendly value (map[string]any / []any / scalar). Returns nil
// when the input is nil or fails to round-trip.
func normalizeArgsValue(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// normalizeResultValue returns a JSON-friendly snapshot of a tool result.
func normalizeResultValue(r *copilot.ToolExecutionCompleteResult) any {
	if r == nil {
		return nil
	}
	return normalizeArgsValue(r)
}

// stringifyResult returns a JSON encoding of the tool result, or "" when r is
// nil or marshaling fails. Used to populate ToolEvent.Error on failed calls so
// downstream consumers have a stable, replay-friendly string representation.
func stringifyResult(r *copilot.ToolExecutionCompleteResult) string {
	if r == nil {
		return ""
	}
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}
