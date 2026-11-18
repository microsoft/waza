package main

import (
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
	type result struct {
		code int
		err  error
	}
	output := os.Stdout
	done := make(chan result, 1)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	// Inherited anonymous pipes need not support deadlines. This is only for
	// the exiting CLI process: do not close stdout or change shared handle
	// state, and do not join a blocked write before os.Exit.
	go func() {
		code, err := runnerterminal.NewEmitter(output).Emit(envelope, binding)
		done <- result{code: code, err: err}
	}()
	select {
	case written := <-done:
		return written.code, written.err
	case <-timer.C:
		return 2, fmt.Errorf("writing terminal output: %w", os.ErrDeadlineExceeded)
	}
}
