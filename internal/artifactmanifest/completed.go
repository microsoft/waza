package artifactmanifest

import (
	"context"
	"fmt"
)

// CompletedPublication contains invocation-owned bytes from one verification
// pass, not an atomic cross-object snapshot or execution attestation.
type CompletedPublication struct {
	Manifest      Manifest
	ManifestBytes []byte
	ArtifactBytes map[string][]byte
	Receipt       Receipt
}

// ReadCompleted verifies all declared objects and the final marker in the same
// pass as Inspect, retaining only requested original bytes. Omission selects the
// terminal (at most runnerterminal.MaxEnvelopeBytes); unknown or duplicate names
// are invalid. Selected bytes are bounded by MaxTotalBytes, individual objects
// by MaxArtifactBytes, and the manifest by MaxManifestBytes. Artifact reads are
// bounded by MaxTotalBytes plus one excess-detection byte per declared object.
// Verified but unfinalized declarations remain readable through Inspect only.
func (s *Store) ReadCompleted(ctx context.Context, digest string, names ...string) (*CompletedPublication, error) {
	_, completed, err := s.inspect(ctx, digest, true, names)
	if err != nil {
		return nil, err
	}
	if completed == nil {
		return nil, fmt.Errorf("%w: finalized marker is absent", ErrMissing)
	}
	return completed, nil
}
