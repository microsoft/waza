package orchestration

import (
	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
)

// shellToolRoles lists the built-in host shell tools. MCP tools are excluded
// even when they share a name, because only host-owned shells are tracked.
var shellToolRoles = map[string]string{
	"bash":             models.ShellCallStart,
	"powershell":       models.ShellCallStart,
	"local_shell":      models.ShellCallStart,
	"read_bash":        models.ShellCallRead,
	"read_powershell":  models.ShellCallRead,
	"write_bash":       models.ShellCallWrite,
	"write_powershell": models.ShellCallWrite,
	"stop_bash":        models.ShellCallStop,
	"stop_powershell":  models.ShellCallStop,
}

// buildHostLifecycle projects model-turn boundaries and host shell activity
// from structured session event fields. It returns nil when no events were
// recorded so callers can distinguish unavailable evidence from an observed
// run without shell activity. Like buildToolEvents it is a pure function of
// the recorded events.
func buildHostLifecycle(events []copilot.SessionEvent) *models.HostLifecycle {
	if len(events) == 0 {
		return nil
	}
	out := &models.HostLifecycle{ModelTurns: []models.ModelTurn{}, Shells: []models.ShellLifecycle{}}
	shellIndex := map[string]int{}
	shell := func(id string) *models.ShellLifecycle {
		i, ok := shellIndex[id]
		if !ok {
			i = len(out.Shells)
			shellIndex[id] = i
			out.Shells = append(out.Shells, models.ShellLifecycle{ShellID: id, Calls: []models.ShellToolCall{}, Exits: []models.ShellExitEvent{}})
		}
		return &out.Shells[i]
	}
	type callRef struct {
		shellID string
		call    int
	}
	calls := map[string]callRef{}

	for index, evt := range events {
		if turn, ok := copilotevents.TurnStart(evt); ok {
			out.ModelTurns = append(out.ModelTurns, models.ModelTurn{
				TurnID: turn.TurnID, ParentToolCallID: cloneString(turn.ParentToolCallID), StartIndex: index,
			})
			continue
		}
		if turn, ok := copilotevents.TurnEnd(evt); ok {
			for i := len(out.ModelTurns) - 1; i >= 0; i-- {
				candidate := &out.ModelTurns[i]
				if candidate.EndIndex == nil && candidate.TurnID == turn.TurnID &&
					equalStringPtr(candidate.ParentToolCallID, turn.ParentToolCallID) {
					end := index
					candidate.EndIndex = &end
					break
				}
			}
			continue
		}
		if note, ok := copilotevents.SystemNotification(evt); ok {
			if id, kind, exitCode, ok := shellNotification(note.Kind); ok && id != "" {
				s := shell(id)
				s.Exits = append(s.Exits, models.ShellExitEvent{
					Source: models.ShellExitSourceNotification, Kind: kind, ExitCode: exitCode, Index: index,
				})
			}
			continue
		}
		if start, ok := copilotevents.ToolStart(evt); ok {
			role, known := shellToolRoles[start.ToolName]
			if !known || start.MCPServerName != nil || start.ToolCallID == "" {
				continue
			}
			if _, seen := calls[start.ToolCallID]; seen {
				continue
			}
			args, _ := normalizeArgsValue(start.Arguments).(map[string]any)
			id, _ := args["shellId"].(string)
			if id == "" {
				continue
			}
			call := models.ShellToolCall{
				ToolCallID: start.ToolCallID, ToolName: start.ToolName, Role: role,
				TurnID: cloneString(start.TurnID), StartIndex: index,
			}
			if mode, ok := args["mode"].(string); ok {
				call.Mode = &mode
			}
			if detach, ok := args["detach"].(bool); ok {
				call.Detach = &detach
			}
			s := shell(id)
			s.Calls = append(s.Calls, call)
			calls[start.ToolCallID] = callRef{shellID: id, call: len(s.Calls) - 1}
			continue
		}
		if complete, ok := copilotevents.ToolComplete(evt); ok {
			ref, tracked := calls[complete.ToolCallID]
			if !tracked {
				continue
			}
			s := shell(ref.shellID)
			call := &s.Calls[ref.call]
			if call.CompleteIndex != nil {
				continue
			}
			completeIndex, success := index, complete.Success
			call.CompleteIndex, call.Success = &completeIndex, &success
			if complete.ShellExecution != nil {
				exitCode := complete.ShellExecution.ExitCode
				s.Exits = append(s.Exits, models.ShellExitEvent{
					Source: models.ShellExitSourceToolExecution, Kind: "shell_exit", ExitCode: &exitCode,
					Index: index, ToolCallID: complete.ToolCallID,
				})
			}
			if complete.Result == nil {
				continue
			}
			for _, content := range complete.Result.Contents {
				exit, ok := shellExitContent(content)
				if !ok || exit.ShellID == "" {
					continue
				}
				exitCode := exit.ExitCode
				target := shell(exit.ShellID)
				target.Exits = append(target.Exits, models.ShellExitEvent{
					Source: models.ShellExitSourceToolResult, Kind: "shell_exit", ExitCode: &exitCode,
					Index: index, ToolCallID: complete.ToolCallID,
				})
			}
		}
	}
	return out
}

func shellNotification(kind copilot.SystemNotification) (shellID, kindName string, exitCode *int64, ok bool) {
	switch k := kind.(type) {
	case *copilot.SystemNotificationShellCompleted:
		return k.ShellID, string(k.Type()), cloneInt64(k.ExitCode), true
	case copilot.SystemNotificationShellCompleted:
		return k.ShellID, string(k.Type()), cloneInt64(k.ExitCode), true
	case *copilot.SystemNotificationShellDetachedCompleted:
		return k.ShellID, string(k.Type()), nil, true
	case copilot.SystemNotificationShellDetachedCompleted:
		return k.ShellID, string(k.Type()), nil, true
	default:
		return "", "", nil, false
	}
}

func shellExitContent(content copilot.ToolExecutionCompleteContent) (copilot.ToolExecutionCompleteContentShellExit, bool) {
	switch c := content.(type) {
	case *copilot.ToolExecutionCompleteContentShellExit:
		if c == nil {
			return copilot.ToolExecutionCompleteContentShellExit{}, false
		}
		return *c, true
	case copilot.ToolExecutionCompleteContentShellExit:
		return c, true
	default:
		return copilot.ToolExecutionCompleteContentShellExit{}, false
	}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
