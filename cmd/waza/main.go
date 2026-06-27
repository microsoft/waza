package main

import (
	"errors"
	"fmt"
	"os"
)

// Exit codes for different failure modes.
//
// Existing commands (waza run) historically use 0/1/2 for success /
// test failure / config error. The `waza gate` command defines its own
// stable scheme documented in cmd_gate.go: 0=pass, 1=regression,
// 2=golden failure, 3=config error.
const (
	ExitSuccess    = 0 // All tests passed
	ExitTestFailed = 1 // One or more tests failed
	ExitError      = 2 // Configuration or runtime error
)

// TestFailureError indicates that the benchmark ran successfully,
// but one or more test cases failed validation.
type TestFailureError struct {
	Message string
}

func (e *TestFailureError) Error() string {
	return e.Message
}

func main() {
	if err := execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)

		// Gate-specific exit codes (0=pass, 1=regression, 2=golden, 3=config).
		// See cmd_gate.go for the full contract.
		var gateErr *gateExitError
		if errors.As(err, &gateErr) {
			os.Exit(gateErr.code)
		}

		// Check error type to determine exit code
		var testFailureErr *TestFailureError
		if errors.As(err, &testFailureErr) {
			os.Exit(ExitTestFailed)
		}

		// All other errors are configuration/runtime errors
		os.Exit(ExitError)
	}
}
