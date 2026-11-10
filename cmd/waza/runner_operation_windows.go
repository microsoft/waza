package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

func emitInheritedRunnerJSON(data []byte) error {
	payload := bytes.Clone(data)
	output := os.Stdout
	done := make(chan error, 1)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	go func() {
		written, err := output.Write(payload)
		if err == nil && written != len(payload) {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("writing runner operation output: %w", os.ErrDeadlineExceeded)
	}
}
