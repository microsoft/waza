package execution

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/commandmock"
)

func commandMockToolHook(session *commandmock.Session) copilot.PreToolUseHandler {
	if session == nil {
		return nil
	}
	return func(input copilot.PreToolUseHookInput, _ copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
		if input.ToolName != "bash" && input.ToolName != "powershell" && input.ToolName != "local_shell" {
			return nil, nil
		}
		args, err := toolArgsMap(input.ToolArgs)
		if err != nil {
			return nil, fmt.Errorf("preparing command-mock shell environment: %w", err)
		}
		command, ok := args["command"].(string)
		if !ok {
			return nil, fmt.Errorf("preparing command-mock shell environment: tool %q has no string command", input.ToolName)
		}
		args["command"] = commandMockEnvironmentPrefix(input.ToolName, session.ID()) + command
		return &copilot.PreToolUseHookOutput{ModifiedArgs: args}, nil
	}
}

func toolArgsMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var args map[string]any
	if err := json.Unmarshal(data, &args); err != nil {
		return nil, err
	}
	if args == nil {
		return nil, fmt.Errorf("tool arguments are empty")
	}
	return args, nil
}

func commandMockEnvironmentPrefix(toolName, sessionID string) string {
	if toolName == "powershell" || toolName == "local_shell" && runtime.GOOS == "windows" {
		return "$env:" + commandmock.SessionEnvironmentVariable + "='" +
			strings.ReplaceAll(sessionID, "'", "''") + "'; "
	}
	return "export " + commandmock.SessionEnvironmentVariable + "='" +
		strings.ReplaceAll(sessionID, "'", "'\"'\"'") + "'; "
}

func combinePreToolUseHandlers(first, second copilot.PreToolUseHandler) copilot.PreToolUseHandler {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return func(input copilot.PreToolUseHookInput, invocation copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
		output, err := first(input, invocation)
		if err != nil || output != nil {
			return output, err
		}
		return second(input, invocation)
	}
}
