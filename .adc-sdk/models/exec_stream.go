// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

// ExecStreamMessageType represents the type of WebSocket message.
type ExecStreamMessageType string

const (
	// ExecStreamMessageTypeSessionID is received with the session ID.
	ExecStreamMessageTypeSessionID ExecStreamMessageType = "session_id"
	// ExecStreamMessageTypeStdout is received with stdout output.
	ExecStreamMessageTypeStdout ExecStreamMessageType = "stdout"
	// ExecStreamMessageTypeStderr is received with stderr output.
	ExecStreamMessageTypeStderr ExecStreamMessageType = "stderr"
	// ExecStreamMessageTypeExitCode is received when the command completes.
	ExecStreamMessageTypeExitCode ExecStreamMessageType = "exit_code"
	// ExecStreamMessageTypeError is received when an error occurs.
	ExecStreamMessageTypeError ExecStreamMessageType = "error"
)

// ExecStreamStartRequest is the request to start an exec stream session.
type ExecStreamStartRequest struct {
	// Command is the command to execute.
	Command string
	// Args are the command arguments.
	Args []string
	// Environment contains environment variables.
	Environment map[string]string
	// WorkingDirectory is the working directory for the command.
	WorkingDirectory string
	// Tty enables pseudo-terminal mode.
	Tty bool
	// Stdin enables stdin input.
	Stdin bool
	// Height is the terminal height (for TTY mode).
	Height int
	// Width is the terminal width (for TTY mode).
	Width int
	// User is the username to run the command as (e.g., "node", "www-data"). Defaults to root.
	User string
}

// ExecStreamMessage is a message received from an exec stream.
type ExecStreamMessage struct {
	// Type is the message type.
	Type ExecStreamMessageType
	// SessionID is the session ID (for session_id messages).
	SessionID string
	// Data is the output data (for stdout/stderr messages).
	Data []byte
	// ExitCode is the exit code (for exit_code messages).
	ExitCode *int
	// Error is the error message (for error messages).
	Error string
}
