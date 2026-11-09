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

// BenchmarkError retains completed benchmark artifacts while reporting an
// operational failure rather than a completed negative judgment.
type BenchmarkError struct {
	Message string
}

func (e *BenchmarkError) Error() string {
	return e.Message
}

func completedBenchmarkError(err error) bool {
	_, runtimeError := errors.AsType[*BenchmarkError](err)
	_, validationError := errors.AsType[*TestFailureError](err)
	return runtimeError || validationError
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
	// Cobra and the final diagnostic share one process-only budget. Diagnostic
	// delivery failures must not replace the command's selected exit status.
	diagnostics := &processDiagnosticWriter{output: processStderr{}}
	if err := execute(diagnostics); err != nil {
		// ExitCodeError takes precedence — the subcommand picked the code.
		var exitErr *ExitCodeError
		if errors.As(err, &exitErr) {
			if exitErr.Err != nil && exitErr.Err.Error() != "" {
				if _, diagnosticErr := fmt.Fprintln(diagnostics, exitErr.Err.Error()); diagnosticErr != nil {
					os.Exit(exitErr.Code)
				}
			}
			os.Exit(exitErr.Code)
		}

		// Check error type to determine exit code
		code := ExitError
		var testFailureErr *TestFailureError
		_, benchmarkError := errors.AsType[*BenchmarkError](err)
		if !benchmarkError && errors.As(err, &testFailureErr) {
			code = ExitTestFailed
		}
		if _, diagnosticErr := fmt.Fprintln(diagnostics, err); diagnosticErr != nil {
			os.Exit(code)
		}
		os.Exit(code)
	}
}
