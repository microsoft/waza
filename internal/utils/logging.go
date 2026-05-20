package utils

import (
	"context"
	"log/slog"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
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
		case copilot.SessionEventTypePendingMessagesModified,
			copilot.SessionEventTypeHookEnd,
			copilot.SessionEventTypeHookStart:
			// we just drop these from logging, they're mostly noise, or have other events (like tool calls)
			// that are more informative.
			return
		case copilot.SessionEventTypeToolExecutionStart:
			if d, ok := event.Data.(*copilot.ToolExecutionStartData); ok && d.ToolName == "report_intent" {
				// store this off, we'll ignore the complete event when it comes in as well.
				intentCalls.Store(d.ToolCallID, true)
				return
			}
		case copilot.SessionEventTypeToolExecutionComplete:
			if d, ok := event.Data.(*copilot.ToolExecutionCompleteData); ok && intentCalls.CompareAndDelete(d.ToolCallID, true) {
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

	attrs := []any{"type", event.Type()}

	switch d := event.Data.(type) {
	case *copilot.AssistantMessageData:
		attrs = append(attrs, "content", d.Content)
		attrs = appendIf(attrs, "reasoningText", d.ReasoningText)
	case *copilot.AssistantMessageDeltaData:
		attrs = append(attrs, "deltaContent", d.DeltaContent)
	case *copilot.UserMessageData:
		attrs = append(attrs, "content", d.Content)
	case *copilot.SkillInvokedData:
		attrs = append(attrs, "name", d.Name, "path", d.Path)
	case *copilot.SessionErrorData:
		attrs = append(attrs, "message", d.Message)
	case *copilot.ToolExecutionStartData:
		attrs = append(attrs, "toolName", d.ToolName, "toolCallID", d.ToolCallID)
		attrs = appendMapOfStringAnyIf(attrs, d.Arguments, "arguments")
	case *copilot.ToolUserRequestedData:
		attrs = append(attrs, "toolName", d.ToolName, "toolCallID", d.ToolCallID)
		attrs = appendMapOfStringAnyIf(attrs, d.Arguments, "arguments")
	case *copilot.ToolExecutionProgressData:
		attrs = append(attrs, "toolCallID", d.ToolCallID, "message", d.ProgressMessage)
	case *copilot.ToolExecutionCompleteData:
		attrs = append(attrs, "toolCallID", d.ToolCallID, "success", d.Success)
		if d.Error != nil {
			attrs = append(attrs, "message", d.Error.Message)
		}
		if d.Result != nil {
			attrs = append(attrs, slog.Any("toolResult", d.Result))
		}
	case *copilot.ToolExecutionPartialResultData:
		attrs = append(attrs, "toolCallID", d.ToolCallID, "message", d.PartialOutput)
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
