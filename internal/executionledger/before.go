package executionledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// PublishBeforeDispatch serializes a fresh creator's immutable publication with
// dispatch and completion attempts. publish must not call Distinct or Complete:
// the same creator gate remains held until publication and final checks finish.
// The mutex wait and a blocked provider are not made cancellable by this method.
// This in-process boundary does not establish recovery or a worker lease.
func (l *Ledger) PublishBeforeDispatch(ctx context.Context, capability Creator, publish func(context.Context) error) error {
	reserved, err := l.Reservation(capability)
	if err != nil {
		return err
	}
	if publish == nil {
		return fmt.Errorf("%w: before-dispatch publication is unavailable", ErrContext)
	}
	capability.state.mu.Lock()
	defer capability.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if capability.state.attempted {
		return fmt.Errorf("%w: dispatch or completion was already attempted by this creator", ErrContext)
	}
	check := func() error {
		if err := l.ValidateCreator(ctx, capability); err != nil {
			return err
		}
		_, readErr := l.ReadCompletion(ctx, reserved.Attempt.ExecutionID)
		err := readErr
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(err) {
			err = cause
		}
		if err != artifactmanifest.ErrMissing {
			if readErr != nil {
				return fmt.Errorf("%w: before-dispatch completion absence is not established: %w", ErrContext, readErr)
			}
			return fmt.Errorf("%w: completion already exists before dispatch", ErrContext)
		}
		return ctx.Err()
	}
	if err := check(); err != nil {
		return err
	}
	if err := publish(ctx); err != nil {
		return err
	}
	return check()
}
