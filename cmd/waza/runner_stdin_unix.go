//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"golang.org/x/sys/unix"
)

type runnerInputResult struct {
	data []byte
	err  error
}

type runnerOutputResult struct {
	err error
}

// Inherited descriptors may share their open file description with a parent or
// peer. Keep their flags untouched; the CLI exits after either result wins.
func readInheritedRunnerInput(ctx context.Context) ([]byte, error) {
	return readInheritedRunnerInputLimit(ctx, runnercontract.MaxRequestBytes)
}

func readInheritedRunnerInputLimit(ctx context.Context, maxBytes int64) ([]byte, error) {
	result := make(chan runnerInputResult, 1)
	go func() {
		read := func() ([]byte, error) { return readInheritedRunnerRequestLimit(maxBytes) }
		if maxBytes == runnercontract.MaxRequestBytes {
			read = readInheritedRunnerRequest
		}
		data, err := read()
		result <- runnerInputResult{data: data, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("runner input interrupted: %w", ctx.Err())
	case read := <-result:
		if cause := ctx.Err(); cause != nil {
			return nil, fmt.Errorf("runner input interrupted: %w", cause)
		}
		if read.err != nil {
			return nil, fmt.Errorf("reading runner request: %w", read.err)
		}
		return read.data, nil
	}
}

// Signals can interrupt a blocking Unix read before signal.NotifyContext has
// scheduled its cancellation. Retrying EINTR lets that cancellation win instead
// of converting SIGINT/SIGTERM into an infrastructure failure.
func readInheritedRunnerRequest() ([]byte, error) {
	return readInheritedRunnerRequestLimit(runnercontract.MaxRequestBytes)
}

func readInheritedRunnerRequestLimit(maxBytes int64) ([]byte, error) {
	var data bytes.Buffer
	buffer := make([]byte, 32<<10)
	limit := maxBytes + 1
	for int64(data.Len()) < limit {
		remaining := limit - int64(data.Len())
		readBuffer := buffer
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		n, err := os.Stdin.Read(readBuffer)
		if n > 0 {
			if _, writeErr := data.Write(readBuffer[:n]); writeErr != nil {
				return nil, writeErr
			}
		}
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err == nil:
			continue
		case errors.Is(err, io.EOF):
			return data.Bytes(), nil
		default:
			return nil, err
		}
	}
	return data.Bytes(), nil
}

// The terminal record is prepared before the one asynchronous Write attempt.
// The process exits on timeout, so a blocked inherited descriptor cannot retain
// a goroutine or cause a later fallback record.
func emitInheritedRunnerTerminal(envelope runnerterminal.Envelope, binding *runnerterminal.Binding) (int, error) {
	data, err := runnerterminal.Encode(envelope, binding)
	if err != nil {
		return 2, err
	}
	if err := emitInheritedRunnerJSON(data); err != nil {
		return 2, err
	}
	return envelope.ExitCode, nil
}

func emitInheritedRunnerJSON(data []byte) error {
	fd, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		return fmt.Errorf("duplicating terminal output: %w", err)
	}
	output := os.NewFile(uintptr(fd), "runner-terminal-output")
	result := make(chan runnerOutputResult, 1)
	go func() {
		payload := bytes.Clone(data)
		written, writeErr := output.Write(payload)
		if writeErr == nil && written != len(payload) {
			writeErr = io.ErrShortWrite
		}
		result <- runnerOutputResult{err: errors.Join(writeErr, output.Close())}
	}()
	select {
	case output := <-result:
		return output.err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("writing terminal output: i/o timeout")
	}
}
