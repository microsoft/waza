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
)

func readInheritedRunnerInput(ctx context.Context) (data []byte, resultErr error) {
	return readInheritedRunnerInputLimit(ctx, runnercontract.MaxRequestBytes)
}

func readInheritedRunnerInputLimit(ctx context.Context, maxBytes int64) (data []byte, resultErr error) {
	if err := os.Stdin.SetReadDeadline(time.Time{}); err == nil {
		finished := make(chan struct{})
		var interruptErr error
		stop := context.AfterFunc(ctx, func() {
			defer close(finished)
			if err := os.Stdin.SetReadDeadline(time.Now()); err != nil {
				interruptErr = fmt.Errorf("interrupting runner input: %w", err)
			}
		})
		defer func() {
			if !stop() {
				<-finished
			}
			resultErr = errors.Join(resultErr, interruptErr)
			if err := os.Stdin.SetReadDeadline(time.Time{}); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("resetting runner input deadline: %w", err))
			}
		}()
	} else {
		finished := make(chan struct{})
		var interruptErr error
		stop := context.AfterFunc(ctx, func() {
			defer close(finished)
			if err := os.Stdin.Close(); err != nil {
				interruptErr = fmt.Errorf("closing interrupted runner stdin: %w", err)
			}
		})
		defer func() {
			if !stop() {
				<-finished
			}
			resultErr = errors.Join(resultErr, interruptErr)
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxBytes+1))
	if cause := ctx.Err(); cause != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", cause)
	}
	if err != nil {
		return nil, fmt.Errorf("reading runner request: %w", err)
	}
	return data, nil
}

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
	type result struct {
		err error
	}
	output := os.Stdout
	done := make(chan result, 1)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	// Inherited anonymous pipes need not support deadlines. This is only for
	// the exiting CLI process: do not close stdout or change shared handle
	// state, and do not join a blocked write before os.Exit.
	go func() {
		payload := bytes.Clone(data)
		written, err := output.Write(payload)
		if err == nil && written != len(payload) {
			err = io.ErrShortWrite
		}
		done <- result{err: err}
	}()
	select {
	case written := <-done:
		return written.err
	case <-timer.C:
		return fmt.Errorf("writing terminal output: %w", os.ErrDeadlineExceeded)
	}
}
