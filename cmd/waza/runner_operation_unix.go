//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func emitInheritedRunnerJSON(data []byte) error {
	fd, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return fmt.Errorf("duplicating runner operation output: %w", err)
	}
	output := os.NewFile(uintptr(fd), "runner-operation-output")
	payload := bytes.Clone(data)
	result := make(chan error, 1)
	go func() {
		written, writeErr := output.Write(payload)
		if writeErr == nil && written != len(payload) {
			writeErr = io.ErrShortWrite
		}
		result <- errors.Join(writeErr, output.Close())
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		return fmt.Errorf("writing runner operation output: %w", os.ErrDeadlineExceeded)
	}
}
