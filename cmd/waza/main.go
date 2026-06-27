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

type exitCoder interface {
	ExitCode() int
}

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

		// Check error type to determine exit code
		var exitCodeErr exitCoder
		if errors.As(err, &exitCodeErr) {
			os.Exit(exitCodeErr.ExitCode())
		}

		var testFailureErr *TestFailureError
		if errors.As(err, &testFailureErr) {
			os.Exit(ExitTestFailed)
		}

		// All other errors are configuration/runtime errors
		os.Exit(ExitError)
	}
}
