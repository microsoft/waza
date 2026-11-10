package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/microsoft/waza/internal/clierrors"
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

type ExitCodeError = clierrors.ExitCodeError

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
