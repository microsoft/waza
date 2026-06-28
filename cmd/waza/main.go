package main

import (
	"errors"
	"fmt"
	"os"
)

// Exit codes for different failure modes
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

// ExitCodeError lets a subcommand request a specific process exit code while
// still returning an error to cobra so the message is printed and usage is
// suppressed. Used by `waza gate` to expose its documented exit code matrix
// (0/1/2/3) independently of cobra's default behavior.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error {
	return e.Err
}

func main() {
	if err := execute(); err != nil {
		// ExitCodeError takes precedence — the subcommand picked the code.
		var exitErr *ExitCodeError
		if errors.As(err, &exitErr) {
			if exitErr.Err != nil && exitErr.Err.Error() != "" {
				fmt.Fprintln(os.Stderr, exitErr.Err.Error())
			}
			os.Exit(exitErr.Code)
		}

		fmt.Fprintln(os.Stderr, err)

		// Check error type to determine exit code
		var testFailureErr *TestFailureError
		if errors.As(err, &testFailureErr) {
			os.Exit(ExitTestFailed)
		}

		// All other errors are configuration/runtime errors
		os.Exit(ExitError)
	}
}
