// Package faultsequence allocates finite, task-private fault steps.
package faultsequence

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"
)

var ErrExhausted = errors.New("fault sequence exhausted")

const MaxDelayMilliseconds = math.MaxInt64 / int64(time.Millisecond)

// Reserve consumes one step using exclusive creation in an existing private
// directory. The directory and step count must stay unchanged until all callers
// finish. Markers survive process exit, but machine-crash durability is not
// guaranteed. Concurrent execution order is independent of reservation order.
// A nonnegative index means consumption occurred, even with an error; -1 means
// this call allocated no step.
func Reserve(ctx context.Context, dir string, steps int) (int, error) {
	if steps <= 0 {
		return -1, fmt.Errorf("fault sequence must contain at least one step")
	}
	if !filepath.IsAbs(dir) {
		return -1, fmt.Errorf("fault sequence state directory must be an absolute private path")
	}
	for index := range steps {
		if err := ctx.Err(); err != nil {
			return -1, fmt.Errorf("reserving fault sequence step: %w", err)
		}
		path := filepath.Join(dir, fmt.Sprintf("step-%08d", index))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return -1, fmt.Errorf("reserving fault sequence step %d: %w", index, err)
		}
		// Successful creation consumes the step even if close or later delivery fails.
		if err := file.Close(); err != nil {
			return index, fmt.Errorf("closing reserved fault sequence step %d: %w", index, err)
		}
		return index, nil
	}
	if err := ctx.Err(); err != nil {
		return -1, fmt.Errorf("reserving fault sequence step: %w", err)
	}
	return -1, ErrExhausted
}

// Delay waits without retaining a reservation lock. Cancellation never resets
// a step previously allocated by Reserve.
func Delay(ctx context.Context, milliseconds int64) error {
	if milliseconds < 0 || milliseconds > MaxDelayMilliseconds {
		return fmt.Errorf("fault sequence delay_ms must be between 0 and %d", MaxDelayMilliseconds)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("waiting for fault sequence delay: %w", err)
	}
	if milliseconds == 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for fault sequence delay: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for fault sequence delay: %w", ctx.Err())
	}
}
