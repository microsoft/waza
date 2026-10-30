package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// decodeEventLines decodes native wire-format events so tests exercise the SDK
// union decoding for notification kinds and tool result content blocks.
func decodeEventLines(t *testing.T, lines ...string) []copilot.SessionEvent {
	t.Helper()
	events := make([]copilot.SessionEvent, 0, len(lines))
	for _, line := range lines {
		var event copilot.SessionEvent
		require.NoError(t, json.Unmarshal([]byte(line), &event), line)
		events = append(events, event)
	}
	return events
}

func wireEvent(eventType, data string) string {
	return `{"id":"e","timestamp":"2026-01-01T00:00:00Z","parentId":null,"type":"` + eventType + `","data":` + data + `}`
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }
func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }

// attachedShellEvents is a generic attached background shell: the agent starts
// a command asynchronously, the host reports its exit in a model-facing
// notification, the next model turn reads it, and the session goes idle.
func attachedShellEvents(t *testing.T) []copilot.SessionEvent {
	return decodeEventLines(t,
		wireEvent("assistant.turn_start", `{"turnId":"0"}`),
		wireEvent("tool.execution_start", `{"toolCallId":"start","toolName":"bash","turnId":"0","arguments":{"command":"make check","mode":"async","shellId":"build"}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"start","success":true,"turnId":"0","result":{"content":"<command started in background with shellId: build>"}}`),
		wireEvent("assistant.turn_end", `{"turnId":"0"}`),
		wireEvent("system.notification", `{"content":"<system_notification>done</system_notification>","kind":{"type":"shell_completed","shellId":"build","exitCode":3,"description":"check"}}`),
		wireEvent("assistant.turn_start", `{"turnId":"1"}`),
		wireEvent("tool.execution_start", `{"toolCallId":"read","toolName":"read_bash","turnId":"1","arguments":{"shellId":"build","delay":0}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"read","success":true,"turnId":"1","result":{"content":"failed","contents":[{"type":"shell_exit","shellId":"build","exitCode":3}]}}`),
		wireEvent("assistant.turn_end", `{"turnId":"1"}`),
		wireEvent("session.idle", `{}`),
	)
}

type lifecycleEventsEngine struct {
	execution.AgentEngine
	events []copilot.SessionEvent
}

func (e *lifecycleEventsEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return &execution.ExecutionResponse{
		Success: true, ModelID: "mock", SessionID: "lifecycle", FinalOutput: "exit code 3",
		Events: copilotevents.FromSDK(e.events),
	}, nil
}

func TestRunBenchmarkRecordsHostLifecycleForGraders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events func(*testing.T) []copilot.SessionEvent
	}{
		{"native events", attachedShellEvents},
		{"no native events", func(*testing.T) []copilot.SessionEvent { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`id: shell
name: Background shell
inputs:
  prompt: Run the check in the background and report its exit code.
`), 0o600))
			spec := &models.EvalSpec{
				SpecIdentity: models.SpecIdentity{Name: "lifecycle"},
				Tasks:        []string{"task.yaml"},
				Config:       models.Config{EngineType: "mock", ModelID: "mock", TrialsPerTask: 1, TimeoutSec: 30},
				Graders: []models.GraderConfig{{
					Kind: models.GraderKindInlineScript, Identifier: "exit-delivered",
					Parameters: models.InlineScriptGraderParameters{Language: models.LanguageJavascript, Assertions: []string{
						"host_lifecycle !== null",
						"host_lifecycle.shells[0].exits.some(e => e.source === 'system.notification' && e.exit_code === 3 && host_lifecycle.model_turns.some(turn => turn.start_index > e.index))",
						"host_lifecycle.shells[0].calls.some(c => c.role === 'read' && c.start_index > host_lifecycle.shells[0].exits[0].index)",
					}},
				}},
			}
			events := tc.events(t)
			engine := &lifecycleEventsEngine{AgentEngine: execution.NewMockEngine("mock"), events: events}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine)
			outcome, err := runner.RunBenchmark(t.Context())
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, buildHostLifecycle(events), run.HostLifecycle)
			present := events != nil
			require.Equal(t, present, run.HostLifecycle != nil)
			require.Equal(t, present, run.Validations["exit-delivered"].Passed, "%+v", run.Validations)

			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			restored, err := models.ParseEvaluationOutcome(data, "results.json")
			require.NoError(t, err)
			require.Equal(t, run.HostLifecycle, restored.TestOutcomes[0].Runs[0].HostLifecycle)
		})
	}
}

func TestBuildHostLifecycle_EmptyIsUnavailable(t *testing.T) {
	require.Nil(t, buildHostLifecycle(nil))
	require.Nil(t, buildHostLifecycle([]copilot.SessionEvent{}))
}

func TestBuildHostLifecycle_AttachedBackgroundShell(t *testing.T) {
	got := buildHostLifecycle(attachedShellEvents(t))
	require.Equal(t, &models.HostLifecycle{
		ModelTurns: []models.ModelTurn{
			{TurnID: "0", StartIndex: 0, EndIndex: intPtr(3)},
			{TurnID: "1", StartIndex: 5, EndIndex: intPtr(8)},
		},
		Shells: []models.ShellLifecycle{{
			ShellID: "build",
			Calls: []models.ShellToolCall{
				{ToolCallID: "start", ToolName: "bash", Role: models.ShellCallStart, Mode: strPtr("async"), TurnID: strPtr("0"),
					StartIndex: 1, CompleteIndex: intPtr(2), Success: boolPtr(true)},
				{ToolCallID: "read", ToolName: "read_bash", Role: models.ShellCallRead, TurnID: strPtr("1"),
					StartIndex: 6, CompleteIndex: intPtr(7), Success: boolPtr(true)},
			},
			Exits: []models.ShellExitEvent{
				{Source: models.ShellExitSourceNotification, Kind: "shell_completed", ExitCode: int64Ptr(3), Index: 4},
				{Source: models.ShellExitSourceToolResult, Kind: "shell_exit", ExitCode: int64Ptr(3), Index: 7, ToolCallID: "read"},
			},
		}},
	}, got)
}

func TestBuildHostLifecycle_ExitCodePresenceAndDetachedCompletion(t *testing.T) {
	got := buildHostLifecycle(decodeEventLines(t,
		wireEvent("tool.execution_start", `{"toolCallId":"a","toolName":"powershell","arguments":{"command":"serve","mode":"async","detach":true,"shellId":"srv"}}`),
		wireEvent("system.notification", `{"content":"x","kind":{"type":"shell_completed","shellId":"job"}}`),
		wireEvent("system.notification", `{"content":"x","kind":{"type":"shell_detached_completed","shellId":"srv"}}`),
		wireEvent("system.notification", `{"content":"x","kind":{"type":"agent_idle","agentId":"a","agentType":"general"}}`),
	))
	require.NotNil(t, got)
	require.Empty(t, got.ModelTurns)
	require.Len(t, got.Shells, 2)

	srv := got.Shells[0]
	require.Equal(t, "srv", srv.ShellID)
	require.Len(t, srv.Calls, 1)
	require.Equal(t, boolPtr(true), srv.Calls[0].Detach)
	require.Nil(t, srv.Calls[0].TurnID, "absent turnId must stay absent")
	require.Nil(t, srv.Calls[0].CompleteIndex, "a start without completion is not reported as completed")
	require.Nil(t, srv.Calls[0].Success)
	require.Equal(t, []models.ShellExitEvent{{Source: models.ShellExitSourceNotification, Kind: "shell_detached_completed", Index: 2}}, srv.Exits)

	job := got.Shells[1]
	require.Equal(t, "job", job.ShellID)
	require.Empty(t, job.Calls)
	require.Len(t, job.Exits, 1)
	require.Nil(t, job.Exits[0].ExitCode, "a missing host exit code must not become zero")
}

func TestBuildHostLifecycle_DoesNotInferFromTextArgvOrMCPTools(t *testing.T) {
	got := buildHostLifecycle(decodeEventLines(t,
		wireEvent("tool.execution_start", `{"toolCallId":"mcp","toolName":"bash","mcpServerName":"remote","arguments":{"shellId":"build"}}`),
		wireEvent("tool.execution_start", `{"toolCallId":"sync","toolName":"bash","arguments":{"command":"echo shellId build"}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"sync","success":true,"result":{"content":"<shellId: build completed with exit code 0>"}}`),
		wireEvent("tool.execution_start", `{"toolCallId":"other","toolName":"custom","arguments":{"shellId":"build"}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"other","success":true,"result":{"content":"","contents":[{"type":"shell_exit","shellId":"build","exitCode":0}]}}`),
	))
	require.NotNil(t, got)
	require.Empty(t, got.Shells, "only structured host shell calls and notifications are evidence")
}

func TestBuildHostLifecycle_ReusedShellIDKeepsEveryCall(t *testing.T) {
	got := buildHostLifecycle(decodeEventLines(t,
		wireEvent("tool.execution_start", `{"toolCallId":"one","toolName":"bash","arguments":{"command":"a","mode":"async","shellId":"s"}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"one","success":true,"shellExecution":{"exitCode":0}}`),
		wireEvent("tool.execution_start", `{"toolCallId":"two","toolName":"bash","arguments":{"command":"b","mode":"sync","shellId":"s"}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"two","success":false,"shellExecution":{"exitCode":2}}`),
		wireEvent("tool.execution_complete", `{"toolCallId":"two","success":true}`),
	))
	require.Len(t, got.Shells, 1)
	shell := got.Shells[0]
	require.Len(t, shell.Calls, 2)
	require.Equal(t, strPtr("sync"), shell.Calls[1].Mode)
	require.Equal(t, boolPtr(false), shell.Calls[1].Success, "a duplicate completion must not overwrite the first")
	require.Equal(t, []models.ShellExitEvent{
		{Source: models.ShellExitSourceToolExecution, Kind: "shell_exit", ExitCode: int64Ptr(0), Index: 1, ToolCallID: "one"},
		{Source: models.ShellExitSourceToolExecution, Kind: "shell_exit", ExitCode: int64Ptr(2), Index: 3, ToolCallID: "two"},
	}, shell.Exits)
}

func TestBuildHostLifecycle_SubAgentTurnsMatchTheirOwnEnd(t *testing.T) {
	got := buildHostLifecycle(decodeEventLines(t,
		wireEvent("assistant.turn_start", `{"turnId":"0"}`),
		wireEvent("assistant.turn_start", `{"turnId":"0","parentToolCallId":"task"}`),
		wireEvent("assistant.turn_end", `{"turnId":"0"}`),
		wireEvent("assistant.turn_end", `{"turnId":"0","parentToolCallId":"task"}`),
	))
	require.Equal(t, []models.ModelTurn{
		{TurnID: "0", StartIndex: 0, EndIndex: intPtr(2)},
		{TurnID: "0", ParentToolCallID: strPtr("task"), StartIndex: 1, EndIndex: intPtr(3)},
	}, got.ModelTurns)
}

func TestBuildHostLifecycle_ObservedWithoutShellsSerializesEmptyLists(t *testing.T) {
	got := buildHostLifecycle(decodeEventLines(t, wireEvent("session.idle", `{}`)))
	data, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `{"model_turns":[],"shells":[]}`, string(data))
	require.False(t, strings.Contains(string(data), "null"))
}
