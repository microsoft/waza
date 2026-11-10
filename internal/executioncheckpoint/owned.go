package executioncheckpoint

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
)

// PublishFinalized requires a live creator and its exact original reservation,
// plus the original admitted request and an already-completed publication.
// The runner caller must retain that original request, provide its actual build
// revision and join execution before invoking this function. It does not admit
// or execute a unit, create a completion, or authenticate external runtimes.
func PublishFinalized(ctx context.Context, backend artifactmanifest.Backend, ledger *executionledger.Ledger,
	creator executionledger.Creator, request []byte, revision, manifestDigest string,
) (string, error) {
	checkpoint, reserved, err := creatorDeclaration(ctx, ledger, creator, request, revision, FinalizedUnit)
	if err != nil {
		return "", err
	}
	completed, err := ledger.ReadCompleted(ctx, executionattempt.Source{
		ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: manifestDigest,
	})
	if err != nil {
		return "", fmt.Errorf("checkpoint original completion: %w", err)
	}
	if !bytes.Equal(completed.Reservation.Bytes, reserved.Bytes) {
		return "", fmt.Errorf("%w: completed source differs from the retained creator reservation", ErrContext)
	}
	checkpoint.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	checkpoint.CompletionDigest = artifactmanifest.Hash(completed.CompletionBytes)
	checkpoint.ManifestDigest = manifestDigest
	if err := ValidateContext(checkpoint, reserved.Attempt.Binding, reserved.Bytes, completed.CompletionBytes, completed.Publication.ManifestBytes); err != nil {
		return "", err
	}
	digest, err := Put(ctx, backend, checkpoint)
	if err != nil {
		return digest, err
	}
	if err := ledger.ValidateCreator(ctx, creator); err != nil {
		return digest, fmt.Errorf("checkpoint blob %s was stored but original creator validation failed; preserve unverified evidence: %w", digest, err)
	}
	return digest, nil
}

func creatorDeclaration(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator,
	request []byte, revision, boundary string,
) (Checkpoint, *executionledger.Reservation, error) {
	started, entryContextErr := time.Now(), ctx.Err()
	reserved, err := ledger.Reservation(creator)
	if err != nil {
		return Checkpoint{}, nil, fmt.Errorf("checkpoint creator: %w", err)
	}
	original := bytes.Clone(request)
	compatibilityStarted := time.Now()
	compatibility, err := ForRequest(original, revision)
	if err != nil {
		return Checkpoint{}, nil, err
	}
	compatibilityDone, compatibilityContextErr := time.Now(), ctx.Err()
	declared, err := runnercontract.Parse(original)
	if err != nil {
		return Checkpoint{}, nil, fmt.Errorf("%w: original request selection: %w", ErrContext, err)
	}
	if declared.Selection != reserved.Attempt.Binding.Selection {
		return Checkpoint{}, nil, fmt.Errorf("%w: request selection differs from the creator's original native tuple", ErrContext)
	}
	selectionDone, selectionContextErr := time.Now(), ctx.Err()
	identity, err := runneridentity.DeriveJSON(original)
	if err != nil {
		return Checkpoint{}, nil, fmt.Errorf("%w: original request identity: %w", ErrContext, err)
	}
	if *identity != reserved.Attempt.Binding.Identity {
		return Checkpoint{}, nil, fmt.Errorf("%w: request does not retain the creator's full original identity and native ordinal", ErrContext)
	}
	identityDone, identityContextErr := time.Now(), ctx.Err()
	if err := ledger.ValidateCreator(ctx, creator); err != nil {
		finished := time.Now()
		deadlineRemainingAtEntry := "none"
		if deadline, ok := ctx.Deadline(); ok {
			deadlineRemainingAtEntry = deadline.Sub(started).String()
		}
		return Checkpoint{}, nil, fmt.Errorf("checkpoint creator: %w (declaration_elapsed=%s retained_elapsed=%s compatibility_elapsed=%s selection_elapsed=%s identity_elapsed=%s creator_validation_elapsed=%s deadline_remaining_at_entry=%s entry_ctx_err=%v compatibility_ctx_err=%v selection_ctx_err=%v identity_ctx_err=%v final_ctx_err=%v)",
			err, finished.Sub(started), compatibilityStarted.Sub(started), compatibilityDone.Sub(compatibilityStarted),
			selectionDone.Sub(compatibilityDone), identityDone.Sub(selectionDone), finished.Sub(identityDone),
			deadlineRemainingAtEntry, entryContextErr, compatibilityContextErr, selectionContextErr, identityContextErr, ctx.Err())
	}
	return Checkpoint{
		Kind: "waza.execution-checkpoint", SchemaVersion: Version, Boundary: boundary,
		ExecutionID:       reserved.Attempt.ExecutionID,
		ReservationDigest: reserved.Digest, Compatibility: compatibility,
	}, reserved, nil
}

// ReadStoredFinalized derives the independent original binding and sidecars
// from the actual typed immutable ledger reader, not the checkpoint or a new
// destination ordinal. This retains stored declared-context/content evidence;
// it does not hydrate a creator, attest runtime execution or invoke an engine.
func ReadStoredFinalized(ctx context.Context, ledger *executionledger.Ledger, checkpoint Checkpoint,
	current Compatibility,
) (*executionledger.CompletedExecution, error) {
	if err := ValidateCompatibility(checkpoint, current); err != nil {
		return nil, err
	}
	if checkpoint.Boundary != FinalizedUnit {
		return nil, fmt.Errorf("%w: finalized source read requires finalized-unit; before-unit must use a fresh child attempt", ErrContext)
	}
	completed, err := ledger.ReadCompleted(ctx, executionattempt.Source{
		ExecutionID: checkpoint.ExecutionID, ManifestDigest: checkpoint.ManifestDigest,
	})
	if err != nil {
		return nil, fmt.Errorf("reading original checkpoint source: %w", err)
	}
	if err := ValidateContext(checkpoint, completed.Reservation.Attempt.Binding, completed.Reservation.Bytes,
		completed.CompletionBytes, completed.Publication.ManifestBytes); err != nil {
		return nil, err
	}
	return completed, nil
}
