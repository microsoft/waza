package executionlifecycle

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// Blobs stores only digest-addressed canonical declarations through the existing
// immutable artifact backend. It has no latest pointer, enumeration or ownership.
type Blobs struct {
	backend artifactmanifest.Backend
}

func NewBlobs(backend artifactmanifest.Backend) *Blobs {
	return &Blobs{backend: backend}
}

func (b *Blobs) Put(ctx context.Context, event Event) (string, error) {
	if b == nil || b.backend == nil {
		return "", fmt.Errorf("lifecycle blob backend is unavailable")
	}
	data, err := Encode(event)
	if err != nil {
		return "", err
	}
	digest := artifactmanifest.Hash(data)
	artifact := artifactmanifest.Artifact{
		Name: "lifecycle-observation", SHA256: digest, SizeBytes: int64(len(data)), MediaType: "application/json",
	}
	if err := artifactmanifest.NewStore(b.backend).Put(ctx, artifact, data); err != nil {
		return "", fmt.Errorf("publishing lifecycle observation: %w", err)
	}
	return digest, nil
}

// Load checks bytes/digest/canonical shape, not worker authenticity or ordering.
// Call Apply with the independently retained original admission before using it.
func (b *Blobs) Load(ctx context.Context, digest string) (*Event, error) {
	if b == nil || b.backend == nil {
		return nil, fmt.Errorf("lifecycle blob backend is unavailable")
	}
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("%w: full lowercase SHA-256 digest required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("loading lifecycle observation: %w", err)
	}
	data, err := b.backend.Get(ctx, "blob-"+digest, MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("reading lifecycle observation: %w", err)
	}
	if len(data) > MaxBytes || artifactmanifest.Hash(data) != digest {
		return nil, fmt.Errorf("%w: lifecycle observation digest/size mismatch", artifactmanifest.ErrCorrupt)
	}
	event, err := Parse(data)
	if err != nil {
		return nil, err
	}
	canonical, err := Encode(*event)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: lifecycle observation is not canonical", artifactmanifest.ErrCorrupt)
	}
	return event, nil
}
