package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
)

func emitSubmission(cmd *cobra.Command, result runnerunit.Submission) error {
	data, err := runnerunit.EncodeSubmissionResult(result)
	if err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating runner submission output: %w", err)}
	}
	return emitRunnerOperationJSON(cmd, data, result.ExitCode, result.Status)
}

// The caller owns strict profile validation and protected publication policy.
func emitRunnerOperationJSON(cmd *cobra.Command, data []byte, exitCode int, status string) error {
	if !json.Valid(data) {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("runner operation output is not valid JSON")}
	}
	data = bytes.TrimRight(data, " \t\r\n")
	var err error
	if cmd.OutOrStdout() == os.Stdout {
		err = emitInheritedRunnerJSON(append(data, '\n'))
	} else {
		payload := append(data, '\n')
		var written int
		written, err = cmd.OutOrStdout().Write(payload)
		if err == nil && written != len(payload) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing runner operation: %w", err)}
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode, Err: fmt.Errorf("runner operation: %s", status)}
	}
	return nil
}
