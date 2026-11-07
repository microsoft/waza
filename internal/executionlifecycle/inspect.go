package executionlifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Inspection carries explicit caller-retained original context and known blob
// digests. It is not an authenticated source or a stored owner capability.
type Inspection struct {
	Kind               string                   `json:"kind"`
	SchemaVersion      string                   `json:"schema_version"`
	OriginalAdmission  artifactmanifest.Binding `json:"original_admission"`
	ReservationDigest  string                   `json:"reservation_digest"`
	ObservationDigests []string                 `json:"observation_digests"`
	Deadline           string                   `json:"deadline,omitempty"`
	AsOf               string                   `json:"as_of,omitempty"`
	StaleAfterMS       int64                    `json:"stale_after_ms"`
}

var inspectionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLifecycle("execution-lifecycle-inspection-1.0.schema.json")
})

func ParseInspection(data []byte) (*Inspection, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: inspection context exceeds %d bytes", ErrInvalid, MaxBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing inspection context: %w", ErrInvalid, err)
	}
	schema, err := inspectionSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling inspection schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: inspection schema: %w", ErrInvalid, err)
	}
	var inspection Inspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		return nil, fmt.Errorf("%w: decoding inspection context: %w", ErrInvalid, err)
	}
	for _, text := range []string{inspection.Deadline, inspection.AsOf} {
		if text != "" {
			if _, err := timestamp(text); err != nil {
				return nil, err
			}
		}
	}
	return &inspection, nil
}

// Inspect rereads every caller-known source blob. It never discovers attempts,
// reconstructs source context from storage, executes work or changes evidence.
func Inspect(ctx context.Context, backend artifactmanifest.Backend, original []byte) (Snapshot, error) {
	inspection, err := ParseInspection(original)
	if err != nil {
		return Snapshot{}, err
	}
	if backend == nil {
		return Snapshot{}, fmt.Errorf("lifecycle inspection backend is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("lifecycle inspection: %w", err)
	}
	reservation, err := backend.Get(ctx, "blob-"+inspection.ReservationDigest, MaxBytes)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reading original lifecycle reservation: %w", err)
	}
	if len(reservation) > MaxBytes || artifactmanifest.Hash(reservation) != inspection.ReservationDigest {
		return Snapshot{}, fmt.Errorf("%w: lifecycle reservation digest/size mismatch", artifactmanifest.ErrCorrupt)
	}
	projection, err := New(reservation, *inspection.OriginalAdmission.TerminalBinding(), inspection.Deadline)
	if err != nil {
		return Snapshot{}, err
	}
	blobs := NewBlobs(backend)
	for _, digest := range inspection.ObservationDigests {
		event, err := blobs.Load(ctx, digest)
		if err != nil {
			return Snapshot{}, err
		}
		if err := projection.Apply(*event); err != nil {
			return Snapshot{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("lifecycle inspection: %w", err)
	}
	return projection.Snapshot(inspection.AsOf, time.Duration(inspection.StaleAfterMS)*time.Millisecond)
}
