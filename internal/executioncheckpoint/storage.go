package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// Put stores only a canonical declaration through the existing immutable
// artifact provider. The digest may be returned with an error to locate
// unverified residue; it must not be treated as checkpoint ownership or success.
// The production caller owns safe-boundary and reservation authorization.
func Put(ctx context.Context, backend artifactmanifest.Backend, checkpoint Checkpoint) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("checkpoint publication canceled: %w", err)
	}
	data, err := Encode(checkpoint)
	if err != nil {
		return "", err
	}
	digest := artifactmanifest.Hash(data)
	descriptor := artifactmanifest.Artifact{
		Name: "checkpoint.json", SHA256: digest, SizeBytes: int64(len(data)), MediaType: "application/json",
	}
	if err := artifactmanifest.NewStore(backend).Put(ctx, descriptor, data); err != nil {
		return digest, fmt.Errorf("checkpoint blob %s could not be verified; preserve any stored object and inspect the provider: %w", digest, err)
	}
	if err := ctx.Err(); err != nil {
		return digest, fmt.Errorf("checkpoint blob %s was stored but publication was canceled; preserve the object: %w", digest, err)
	}
	return digest, nil
}

// Load rereads bounded blob bytes and checks hash, schema and canonical encoding.
// It does not infer original ownership, compatible current input, expiry or
// completed units from blob existence. No mutable checkpoint pointer is created.
func Load(ctx context.Context, backend artifactmanifest.Backend, digest string) (*Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("checkpoint read canceled: %w", err)
	}
	schema, err := checkpointSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling checkpoint schema: %w", err)
	}
	if err := schema.Properties["reservation_digest"].Validate(digest); err != nil {
		return nil, fmt.Errorf("%w: checkpoint blob digest must be exactly 64 lowercase hexadecimal characters: %w", ErrInvalid, err)
	}
	if backend == nil {
		return nil, errors.New("checkpoint storage backend is unavailable")
	}
	data, err := backend.Get(ctx, "blob-"+digest, MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint blob %s: %w", digest, err)
	}
	if len(data) > MaxBytes || artifactmanifest.Hash(data) != digest {
		return nil, fmt.Errorf("%w: checkpoint blob %s differs from its digest or exceeds the size limit; preserve evidence and use a clean source", artifactmanifest.ErrCorrupt, digest)
	}
	checkpoint, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: checkpoint blob declaration: %w", artifactmanifest.ErrCorrupt, err)
	}
	canonical, err := Encode(*checkpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding stored checkpoint: %w", artifactmanifest.ErrCorrupt, err)
	}
	if !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: checkpoint blob %s is not canonical; do not normalize stored source evidence", artifactmanifest.ErrCorrupt, digest)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("checkpoint read canceled: %w", err)
	}
	return checkpoint, nil
}
