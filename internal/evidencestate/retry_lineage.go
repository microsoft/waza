package evidencestate

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

// InspectRetryParent reads one immediate declared retry edge, not a complete
// execution graph or runtime attestation. The caller must compose Reader with
// the same invocation budget for its namespace and Store before calling.
// attemptArtifact is the fixed descriptor name used by both capture sources.
// No parent declaration is unavailable, not proof of an empty execution history.
func InspectRetryParent(ctx context.Context, reader *executionledger.Reader, source executionattempt.Source,
	attemptArtifact string,
) (State, *Lineage, error) {
	failure := func(err error) (State, *Lineage, error) {
		state := Unavailable
		if errors.Is(err, artifactmanifest.ErrConflict) || errors.Is(err, executionledger.ErrContext) {
			state = Conflicting
		}
		return state, nil, fmt.Errorf("inspecting declared retry parent: %w", err)
	}
	child, err := reader.ReadCompleted(ctx, source, attemptArtifact)
	if err != nil {
		return failure(err)
	}
	if !bytes.Equal(child.Reservation.Bytes, child.Publication.ArtifactBytes[attemptArtifact]) {
		return failure(artifactmanifest.ErrConflict)
	}
	parent := child.Reservation.Attempt.Parent
	if parent == nil {
		return Unavailable, nil, nil
	}
	if parent.Relationship != string(Retry) {
		return failure(fmt.Errorf("%w: declared parent is not a retry", ErrInvalid))
	}
	completion, err := reader.ReadCompletion(ctx, parent.ExecutionID)
	if err != nil {
		return failure(err)
	}
	parentSource := executionattempt.Source{
		ExecutionID: parent.ExecutionID, ManifestDigest: completion.Completion.ManifestDigest,
	}
	original, err := reader.ReadCompleted(ctx, parentSource, attemptArtifact)
	if err != nil {
		return failure(err)
	}
	if !bytes.Equal(original.Reservation.Bytes, original.Publication.ArtifactBytes[attemptArtifact]) ||
		original.Reservation.Attempt.Logical != child.Reservation.Attempt.Logical ||
		parentSource.ManifestDigest == source.ManifestDigest ||
		(original.Reservation.Attempt.Parent != nil &&
			original.Reservation.Attempt.Parent.ExecutionID == source.ExecutionID) {
		return failure(artifactmanifest.ErrConflict)
	}
	return Present, &Lineage{Manifest: parentSource.ManifestDigest, Relationship: Retry}, nil
}
