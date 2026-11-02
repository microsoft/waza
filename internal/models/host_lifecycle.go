package models

// HostLifecycle is ordered lifecycle evidence projected from the structured
// native session events recorded for a run. It is nil when no native session
// events were recorded, which means the evidence is unavailable rather than
// that nothing happened.
//
// Every Index is the 0-based position of the source event in the run's
// recorded session event stream (including follow-up turns), so values from
// different lists can be compared to order recorded host and model activity.
// This ordering does not verify model-context delivery or model awareness. Only
// structured event fields are used: shell IDs, exit codes and turn IDs are
// never inferred from tool output text, command lines or telemetry. Events the
// host emitted after recording ended (for example a detached shell finishing
// after the session went idle) are absent, so a missing exit is not proof that
// a process is still running.
type HostLifecycle struct {
	// ModelTurns lists each assistant.turn_start in stream order.
	ModelTurns []ModelTurn `json:"model_turns"`
	// Shells groups host shell activity by the shell ID carried in structured
	// tool arguments or host notifications, in order of first appearance.
	Shells []ShellLifecycle `json:"shells"`
}

// ModelTurn is one agent-loop model turn reported by the host.
type ModelTurn struct {
	TurnID string `json:"turn_id"`
	// ParentToolCallID is set when the turn belongs to a sub-agent.
	ParentToolCallID *string `json:"parent_tool_call_id,omitempty"`
	StartIndex       int     `json:"start_index"`
	// EndIndex is the matching assistant.turn_end, omitted when none was recorded.
	EndIndex *int `json:"end_index,omitempty"`
}

// Shell call roles. A start runs a command; read, write and stop address an
// existing shell by ID.
const (
	ShellCallStart = "start"
	ShellCallRead  = "read"
	ShellCallWrite = "write"
	ShellCallStop  = "stop"
)

// Shell exit observation sources.
const (
	// ShellExitSourceNotification is a recorded host-authored system.notification.
	// Its index orders the notification against recorded model turns; it does
	// not verify that the notification reached the model's context.
	ShellExitSourceNotification = "system.notification"
	// ShellExitSourceToolResult is a structured shell_exit block in a recorded
	// shell tool result.
	ShellExitSourceToolResult = "tool_result"
	// ShellExitSourceToolExecution is the shell driver's exit code attached to
	// the completion of the tool call that ran the command.
	ShellExitSourceToolExecution = "tool_execution"
)

// ShellLifecycle is the recorded activity for one host shell ID. A shell ID
// can be reused; more than one start call means the calls and exits cannot be
// attributed to a single process and graders must not assume otherwise.
type ShellLifecycle struct {
	ShellID string           `json:"shell_id"`
	Calls   []ShellToolCall  `json:"calls"`
	Exits   []ShellExitEvent `json:"exits"`
}

// ShellToolCall is a built-in shell tool call that names a shell ID in its
// structured arguments.
type ShellToolCall struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Role       string `json:"role"`
	// Mode and Detach are copied from the call arguments when present.
	Mode   *string `json:"mode,omitempty"`
	Detach *bool   `json:"detach,omitempty"`
	// TurnID is the host turn ID on the start event, omitted when absent.
	TurnID     *string `json:"turn_id,omitempty"`
	StartIndex int     `json:"start_index"`
	// CompleteIndex and Success are omitted when no completion was recorded.
	CompleteIndex *int  `json:"complete_index,omitempty"`
	Success       *bool `json:"success,omitempty"`
}

// ShellExitEvent is one structured observation that a shell finished.
type ShellExitEvent struct {
	Source string `json:"source"`
	// Kind is the notification kind (shell_completed or
	// shell_detached_completed) or shell_exit for tool observations.
	Kind string `json:"kind"`
	// ExitCode is omitted when the host did not report one.
	ExitCode *int64 `json:"exit_code,omitempty"`
	Index    int    `json:"index"`
	// ToolCallID identifies the tool call for tool observations.
	ToolCallID string `json:"tool_call_id,omitempty"`
}
