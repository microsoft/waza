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
	if err := os.Stdout.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, os.ErrNoDeadline) {
		return 2, fmt.Errorf("setting runner output deadline: %w", err)
	}
	return runnerterminal.NewEmitter(os.Stdout).Emit(envelope, binding)
}
