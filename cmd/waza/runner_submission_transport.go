package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Callers validate the strict result profile and apply capture policy first.
func emitRunnerOperationJSON(cmd *cobra.Command, data []byte, exitCode int, status string) error {
	if !json.Valid(data) {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("runner operation output is not valid JSON")}
	}
	payload := append(bytes.Clone(bytes.TrimRight(data, " \t\r\n")), '\n')
	var err error
	if cmd.OutOrStdout() == os.Stdout {
		err = emitInheritedRunnerJSON(payload)
	} else {
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
