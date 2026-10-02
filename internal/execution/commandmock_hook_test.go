package execution

import (
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
)

func TestCommandMockEnvironmentPrefix(t *testing.T) {
	if got := commandMockEnvironmentPrefix("bash", "task-123"); got != "export WAZA_COMMAND_MOCK_SESSION='task-123'; " {
		t.Fatalf("bash prefix = %q", got)
	}
	if got := commandMockEnvironmentPrefix("powershell", "task-123"); got != "$env:WAZA_COMMAND_MOCK_SESSION='task-123'; " {
		t.Fatalf("PowerShell prefix = %q", got)
	}
}

func TestCombinePreToolUseHandlersPreservesPolicyDenial(t *testing.T) {
	secondCalled := false
	handler := combinePreToolUseHandlers(
		func(copilot.PreToolUseHookInput, copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
			return &copilot.PreToolUseHookOutput{PermissionDecision: "deny"}, nil
		},
		func(copilot.PreToolUseHookInput, copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
			secondCalled = true
			return nil, nil
		},
	)
	output, err := handler(copilot.PreToolUseHookInput{}, copilot.HookInvocation{})
	if err != nil {
		t.Fatal(err)
	}
	if output == nil || output.PermissionDecision != "deny" {
		t.Fatalf("unexpected output: %+v", output)
	}
	if secondCalled {
		t.Fatal("second handler ran after policy denial")
	}
}

func TestToolArgsMapCopiesCommand(t *testing.T) {
	args, err := toolArgsMap(map[string]any{"command": "cd /tmp && az account show", "description": "test"})
	if err != nil {
		t.Fatal(err)
	}
	command, ok := args["command"].(string)
	if !ok {
		t.Fatalf("command type = %T", args["command"])
	}
	args["command"] = commandMockEnvironmentPrefix("bash", "task-123") + command
	got, ok := args["command"].(string)
	if !ok {
		t.Fatalf("modified command type = %T", args["command"])
	}
	if !strings.HasPrefix(got, "export WAZA_COMMAND_MOCK_SESSION='task-123'; ") {
		t.Fatalf("command = %q", got)
	}
	if args["description"] != "test" {
		t.Fatalf("description was not preserved: %#v", args)
	}
}

func TestCommandMockToolHookInjectsSessionIdentity(t *testing.T) {
	session, err := commandmock.NewSession(t.TempDir(), []models.CommandMockConfig{{
		Name: "az",
		Responses: []models.CommandMockResponse{{
			Args: []string{"account", "show"},
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = session.Close()
		_ = commandmock.CloseRuntime()
	})

	output, err := commandMockToolHook(session)(copilot.PreToolUseHookInput{
		ToolName: "bash",
		ToolArgs: map[string]any{
			"command":     "cd /tmp && az account show",
			"description": "test",
		},
	}, copilot.HookInvocation{})
	if err != nil {
		t.Fatal(err)
	}
	args, ok := output.ModifiedArgs.(map[string]any)
	if !ok {
		t.Fatalf("modified args type = %T", output.ModifiedArgs)
	}
	wantPrefix := "export WAZA_COMMAND_MOCK_SESSION='" + session.ID() + "'; "
	got, ok := args["command"].(string)
	if !ok {
		t.Fatalf("command type = %T", args["command"])
	}
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("command = %q, want prefix %q", got, wantPrefix)
	}
	if args["description"] != "test" {
		t.Fatalf("description was not preserved: %#v", args)
	}
}
