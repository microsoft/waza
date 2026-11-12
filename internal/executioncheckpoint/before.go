package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

// PublishBefore requires the retained creator and original admitted request.
// The owning single-admission runner must invoke it before constructing an
// engine; an unfinished declaration alone does not prove that lifecycle point.
func PublishBefore(ctx context.Context, backend artifactmanifest.Backend, ledger *executionledger.Ledger,
	creator executionledger.Creator, request []byte, revision string,
) (string, error) {
	declaration, err := DeclareRequestWithContext(ctx, request, revision)
	if err != nil {
		return "", err
	}
	return PublishBeforeDeclared(ctx, backend, ledger, creator, declaration)
}

// PublishBeforeDeclared requires the retained creator and original admitted
// request declaration. The owning runner must call it before engine creation.
func PublishBeforeDeclared(ctx context.Context, backend artifactmanifest.Backend, ledger *executionledger.Ledger,
	creator executionledger.Creator, declaration RequestDeclaration,
) (string, error) {
	checkpoint, reserved, err := creatorDeclaration(ctx, ledger, creator, declaration, BeforeUnit)
	if err != nil {
		return "", err
	}
	checkpoint.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := ValidateContext(checkpoint, reserved.Attempt.Binding, reserved.Bytes, nil, nil); err != nil {
		return "", err
	}
	var digest string
	if err := ledger.PublishBeforeDispatch(ctx, creator, func(ctx context.Context) error {
		var err error
		digest, err = Put(ctx, backend, checkpoint)
		return err
	}); err != nil {
		return digest, fmt.Errorf("before-unit checkpoint publication failed (blob %q may remain); preserve evidence: %w", digest, err)
	}
	return digest, nil
}

// ReadBefore returns original stored declarations, never a creator. Only a
// missing original completion and dispatch decision admit a fresh child attempt.
// The operator must first stop the original worker. This is not a worker lease.
func ReadBefore(ctx context.Context, ledger *executionledger.Ledger, checkpoint Checkpoint,
	current Compatibility,
) (*executionledger.Reservation, error) {
	if err := ValidateCompatibility(checkpoint, current); err != nil {
		return nil, err
	}
	if checkpoint.Boundary != BeforeUnit {
		return nil, fmt.Errorf("%w: before source read requires before-unit; finalized-unit must preserve its original terminal", ErrContext)
	}
	reserved, err := ledger.ReadReservation(ctx, checkpoint.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("reading original checkpoint reservation: %w", err)
	}
	if err := ValidateContext(checkpoint, reserved.Attempt.Binding, reserved.Bytes, nil, nil); err != nil {
		return nil, err
	}
	if err := requireUncompleted(ctx, ledger, reserved); err != nil {
		return nil, err
	}
	return reserved, nil
}

func requireUncompleted(ctx context.Context, ledger *executionledger.Ledger, reserved *executionledger.Reservation) error {
	record, err := ledger.ReadCompletion(ctx, reserved.Attempt.ExecutionID)
	if completionAbsent(err) {
		started, err := ledger.HasDistinctDecision(ctx, reserved.Attempt.ExecutionID)
		if err != nil {
			return fmt.Errorf("%w: original dispatch state is unavailable; preserve the checkpoint: %w", ErrContext, err)
		}
		if started {
			return fmt.Errorf("%w: original dispatch may have executed without a completion; before-unit recovery cannot safely repeat it", ErrStale)
		}
		return nil
	}

	if err != nil {
		return fmt.Errorf("%w: original completion state could not be admitted; preserve the source rather than retrying: %w", ErrContext, err)
	}
	if record == nil || record.Completion.ReservationDigest != reserved.Digest {
		return fmt.Errorf("%w: present completion does not retain the original reservation", ErrContext)
	}
	completed, err := ledger.ReadCompleted(ctx, executionattempt.Source{
		ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: record.Completion.ManifestDigest,
	})
	if err != nil {
		return fmt.Errorf("%w: completion is present but its original publication is not verified finalized; do not treat missing content as an absent completion: %w", ErrContext, err)
	}
	if !bytes.Equal(completed.Reservation.Bytes, reserved.Bytes) || !bytes.Equal(completed.CompletionBytes, record.Bytes) {
		return fmt.Errorf("%w: original completion context changed between source reads", ErrContext)
	}
	return fmt.Errorf("%w: original attempt already has a verified completion; use its finalized checkpoint or original terminal, not this before-unit record", ErrStale)
}

func completionAbsent(err error) bool {
	for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(err) {
		err = cause
	}
	// Aggregate failures cannot establish absence, even if one cause is missing.
	return err == artifactmanifest.ErrMissing
}
