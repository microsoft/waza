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
	if err := os.Stdin.SetReadDeadline(time.Time{}); err == nil {
		finished := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(finished)
			if err := os.Stdin.SetReadDeadline(time.Now()); err != nil {
				fmt.Fprintf(os.Stderr, "interrupting runner input: %v\n", err)
			}
		})
		defer func() {
			if !stop() {
				<-finished
			}
			if err := os.Stdin.SetReadDeadline(time.Time{}); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("resetting runner input deadline: %w", err))
			}
		}()
	} else {
		finished := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(finished)
			if err := os.Stdin.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "closing interrupted runner stdin: %v\n", err)
			}
		})
		defer func() {
			if !stop() {
				<-finished
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, runnercontract.MaxRequestBytes+1))
	if cause := ctx.Err(); cause != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", cause)
	}
	if err != nil {
		return nil, fmt.Errorf("reading runner request: %w", err)
	}
	return data, nil
}

func emitInheritedRunnerTerminal(envelope runnerterminal.Envelope, binding *runnerterminal.Binding) (int, error) {
	return runnerterminal.NewEmitter(runnerTerminalProcessWriter{}).Emit(envelope, binding)
}

type runnerTerminalProcessWriter struct{}

func (runnerTerminalProcessWriter) Write(data []byte) (int, error) {
	owned := bytes.Clone(data)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := os.Stdout.Write(owned)
		if err == nil && n != len(owned) {
			err = io.ErrShortWrite
		}
		done <- result{n: n, err: err}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case written := <-done:
		return written.n, written.err
	case <-timer.C:
		return 0, fmt.Errorf("writing runner terminal output: %w", os.ErrDeadlineExceeded)
	}
}
