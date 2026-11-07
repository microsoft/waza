package executioncheckpoint

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// ReadFinalized compares current compatibility and returns original terminal
// bytes from the actual provider's complete verification pass. Original and the
// sidecars must come from the authenticated source reader, not a new request or
// the checkpoint itself. This helper never runs an engine, reserves an attempt
// or changes the original outcome. The caller owns source authentication.
func ReadFinalized(ctx context.Context, backend artifactmanifest.Backend, checkpoint Checkpoint, current Compatibility, original artifactmanifest.Binding, reservation, completion []byte) (*artifactmanifest.CompletedPublication, error) {
	if err := ValidateCompatibility(checkpoint, current); err != nil {
		return nil, err
	}
	if checkpoint.Boundary != FinalizedUnit {
		return nil, fmt.Errorf("%w: only finalized-unit can read completed source bytes; before-unit requires a fresh owned child attempt", ErrContext)
	}
	publication, err := artifactmanifest.NewStore(backend).ReadCompleted(ctx, checkpoint.ManifestDigest)
	if err != nil {
		return nil, fmt.Errorf("verifying checkpoint original finalized publication: %w", err)
	}
	if err := ValidateContext(checkpoint, original, reservation, completion, publication.ManifestBytes); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("checkpoint finalized-source read canceled: %w", err)
	}
	return publication, nil
}
