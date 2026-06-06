package utils

import (
	"context"
	"log/slog"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
)

// NewSessionToSlog creates a function compatible with [copilot.Session.On] that will
// emit log entries, to slog, when the log level is set to slog.LevelDebug.
func NewSessionToSlog() copilot.SessionEventHandler {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return func(copilot.SessionEvent) {}
	}

	intentCalls := sync.Map{}

	return func(event copilot.SessionEvent) {
		switch event.Type() {
		case copilot.SessionEventTypePendingMessagesModified, copilot.SessionEventTypeHookEnd, copilot.SessionEventTypeHookStart:
			// we just drop these from logging, they're mostly noise, or have other events (like tool calls)
			// that are more informative.
			return
		case copilot.SessionEventTypeToolExecutionStart:
			if data, ok := copilotevents.ToolStart(event); ok && data.ToolName == "report_intent" && data.ToolCallID != "" {
				// store this off, we'll ignore the complete event when it comes in as well.
				intentCalls.Store(data.ToolCallID, true)
				return
			}
		case copilot.SessionEventTypeToolExecutionComplete:
			if toolCallID, ok := copilotevents.ToolCallID(event); ok &&
				intentCalls.CompareAndDelete(toolCallID, true) {
				return
			}
		}

		sessionToSlog(event)
	}
}

// sessionToSlog tries to be a low-overhead method for dumping out any session events coming from
// the copilot client to slog. It's safe to add this to your copilot session instances, in
// their [copilot.Session.On] handler.
func sessionToSlog(event copilot.SessionEvent) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	attrs := []any{
		"type", event.Type(),
	}

	attrs = appendIf(attrs, "reasoningText", copilotevents.ReasoningText(event))

	// session starts
	if start, ok := copilotevents.SessionStart(event); ok {
		attrs = appendIf(attrs, "selectedModel", start.SelectedModel)
		attrs = append(attrs, "producer", start.Producer)
		attrs = append(attrs, "sessionID", start.SessionID)

		if start.Context != nil {
			cc := start.Context
			var ccAttrs []any

			ccAttrs = appendIf(ccAttrs, "branch", cc.Branch)
			ccAttrs = append(ccAttrs, "cwd", cc.Cwd)
			ccAttrs = append(ccAttrs, "gitRoot", ptrValue(cc.GitRoot))
			ccAttrs = append(ccAttrs, "repository", ptrValue(cc.Repository))

			attrs = append(attrs, slog.Group("context", ccAttrs...))
		}
	}

	// assistant.turn_start
	if turn, ok := event.Data.(*copilot.AssistantTurnStartData); ok {
		attrs = append(attrs, "turnID", turn.TurnID)
	}
	if turn, ok := event.Data.(*copilot.AssistantTurnEndData); ok {
		attrs = append(attrs, "turnID", turn.TurnID)
	}

	// tool calls
	if content, ok := copilotevents.Content(event); ok {
		attrs = append(attrs, "content", content)
	}
	if deltaContent, ok := copilotevents.DeltaContent(event); ok {
		attrs = append(attrs, "deltaContent", deltaContent)
	}
	if start, ok := copilotevents.ToolStart(event); ok {
		attrs = append(attrs, "toolName", start.ToolName)
		attrs = append(attrs, "toolCallID", start.ToolCallID)
		attrs = appendMapOfStringAnyIf(attrs, start.Arguments, "arguments")
	}
	if complete, ok := copilotevents.ToolComplete(event); ok {
		attrs = append(attrs, "toolCallID", complete.ToolCallID)
		attrs = append(attrs, "success", complete.Success)
		if complete.Result != nil {
			tr := complete.Result

			var toolResultArgs []any

			toolResultArgs = append(toolResultArgs, "content", tr.Content)
			toolResultArgs = appendIf(toolResultArgs, "detailedContent", tr.DetailedContent)

			attrs = append(attrs, slog.Group("toolResult", toolResultArgs...))
		}
	}

	// hooks
	if hook, ok := copilotevents.HookStart(event); ok {
		attrs = append(attrs, "hookType", hook.HookType)
		attrs = appendMapOfStringAnyIf(attrs, hook.Input, "input")
	}

	slog.Debug("Event received", attrs...)
}

// appendIf appends the attribute if v is not nil
func appendIf[T any](attrs []any, name string, v *T) []any {
	if v != nil {
		attrs = append(attrs, name)
		attrs = append(attrs, *v)
	}

	return attrs
}

func ptrValue[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}

// appendMapOfStringAnyIf appends the contents of the map, as a slog.Group if the
// map is both a map[string]any, and not empty.
// NOTE: the keys are not sorted as they are added to the slog.Group.
func appendMapOfStringAnyIf(attrs []any, mapOfStringAny any, fieldName string) []any {
	if asMap, ok := mapOfStringAny.(map[string]any); ok {
		if len(asMap) == 0 {
			return attrs
		}

		var args []any

		for k, v := range asMap {
			args = append(args, k, v)
		}

		attrs = append(attrs, slog.Group(fieldName, args...))
	}

	return attrs
}
