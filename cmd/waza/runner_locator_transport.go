package main

import (
	"fmt"
	"io"
	"os"

	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/spf13/cobra"
)

type runnerCheckpointLocatorError struct{ cause error }

func (e *runnerCheckpointLocatorError) Error() string {
	return fmt.Sprintf("writing checkpoint locator: %v", e.cause)
}

func (e *runnerCheckpointLocatorError) Unwrap() error { return e.cause }

// This emitter is only for the exiting CLI, not an embedding service. The caller
// must propagate delivery failure; it still owns source admission/disclosure.
func newRunnerCheckpointLocatorEmitter(cmd *cobra.Command) func(boundary, digest string) error {
	output := cmd.ErrOrStderr()
	writer, bounded := output.(*processDiagnosticWriter)
	if !bounded {
		if output == os.Stderr {
			output = processStderr{}
		}
		writer = &processDiagnosticWriter{output: output}
	}
	return func(boundary, digest string) error {
		if boundary != executioncheckpoint.BeforeUnit && boundary != executioncheckpoint.FinalizedUnit {
			return fmt.Errorf("checkpoint locator has unsupported boundary")
		}
		if err := executioncheckpoint.ValidateDigest(digest); err != nil {
			return fmt.Errorf("checkpoint locator requires canonical SHA256: %w", err)
		}
		data := []byte("checkpoint " + boundary + " " + digest + "\n")
		n, err := writer.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return &runnerCheckpointLocatorError{cause: err}
		}
		return nil
	}
}
