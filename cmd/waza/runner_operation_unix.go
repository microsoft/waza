//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func emitInheritedRunnerJSON(data []byte) error {
	return writeInheritedRunnerJSON(os.Stdout, data, 5*time.Second)
}

func writeInheritedRunnerJSON(output *os.File, data []byte, timeout time.Duration) error {
	if runtime.GOOS == "linux" {
		if err := writeInheritedRunnerJSONWithDeadline(output, data, timeout); !errors.Is(err, os.ErrNoDeadline) {
			return err
		}
	}

	// Non-Linux Unix targets may not register inherited descriptors with the
	// poller or reliably interrupt pipe writes via deadlines.
	return writeRunnerOperationOutputChild(output, data, timeout)
}

func writeInheritedRunnerJSONWithDeadline(output *os.File, data []byte, timeout time.Duration) (resultErr error) {
	if err := output.SetWriteDeadline(time.Now().Add(timeout)); err == nil {
		defer func() {
			if err := output.SetWriteDeadline(time.Time{}); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("resetting runner operation output deadline: %w", err))
			}
		}()
		written, err := output.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("writing runner operation output: %w", err)
		}
		return nil
	} else if errors.Is(err, os.ErrNoDeadline) {
		return err
	} else {
		return fmt.Errorf("setting runner operation output deadline: %w", err)
	}
}
