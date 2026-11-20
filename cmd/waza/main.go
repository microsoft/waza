package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/microsoft/waza/internal/clierrors"
	"github.com/microsoft/waza/internal/executionisolation"
	"github.com/microsoft/waza/internal/usagetelemetry"
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
	Cause   error
}

func (e *BenchmarkError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *BenchmarkError) Unwrap() error {
	return e.Cause
}

func completedBenchmarkError(err error) bool {
	_, runtimeError := errors.AsType[*BenchmarkError](err)
	_, validationError := errors.AsType[*TestFailureError](err)
	return runtimeError || validationError
}

type ExitCodeError = clierrors.ExitCodeError

func main() {
	executionisolation.ExitIfSubprocessProbe()
	// Cobra and the final diagnostic share one process-only budget. Diagnostic
	// delivery failures must not replace the command's selected exit status.
	diagnostics := &processDiagnosticWriter{output: processStderr{}}
	os.Exit(runMain(diagnostics, os.Args[1:]))
}

// runMain resolves the usage telemetry policy before any command runs, then
// selects the exit status exactly as before. Telemetry is recorded and flushed
// only after the status and diagnostics are final, so it cannot change them.
func runMain(diagnostics io.Writer, args []string) int {
	started := time.Now()
	policy := usagetelemetry.ResolveProcess(args, false, version)
	usagetelemetry.Propagate(policy)
	usagetelemetry.Start(policy, version)

	cmd, entered, err := execute(diagnostics, args)
	code := reportExit(diagnostics, err)

	// Cobra returns a nil error without running anything for --help and for
	// parent commands that only print help; those are not usage.
	if collectableCommand(cmd) && (entered || err != nil) {
		usagetelemetry.RecordCommand(commandName(cmd), commandOutcome(err, code, entered), time.Since(started))
	}
	usagetelemetry.Shutdown(usagetelemetry.ShutdownTimeout)
	return code
}

func reportExit(diagnostics io.Writer, err error) int {
	if err == nil {
		return ExitSuccess
	}
	// ExitCodeError takes precedence — the subcommand picked the code.
	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		if exitErr.Err != nil && exitErr.Err.Error() != "" {
			_, _ = fmt.Fprintln(diagnostics, exitErr.Err.Error())
		}
		return exitErr.Code
	}

	// Check error type to determine exit code
	code := ExitError
	var testFailureErr *TestFailureError
	_, benchmarkError := errors.AsType[*BenchmarkError](err)
	if !benchmarkError && errors.As(err, &testFailureErr) {
		code = ExitTestFailed
	}
	_, _ = fmt.Fprintln(diagnostics, err)
	return code
}
