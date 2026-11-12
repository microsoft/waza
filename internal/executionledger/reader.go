package executionledger

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
)

type recordReader interface {
	ReadUnverified(context.Context, string) ([]byte, error)
}

type executionReader interface {
	ReadReservation(context.Context, string) (*Reservation, error)
	ReadCompletion(context.Context, string) (*CompletionRecord, error)
}

// Reader verifies original declarations and immutable publication context only.
// It exposes no reservation, completion, decision, or creator capability.
type Reader struct {
	namespace *artifactmanifest.NamespaceReader
	store     *artifactmanifest.Store
}

// NewReader composes an actual scoped namespace reader and a Store backend.
// For a cumulative budget, both must already share the caller's decorator state:
// storeBackend must be the decorated backend, not the raw provider. This
// constructor does not enforce a budget or perform provider I/O.
func NewReader(namespace *artifactmanifest.NamespaceReader, storeBackend artifactmanifest.Backend) (*Reader, error) {
	if namespace == nil || storeBackend == nil {
		return nil, fmt.Errorf("%w: scoped namespace reader and Store backend are required", ErrCapability)
	}
	return &Reader{namespace: namespace, store: artifactmanifest.NewStore(storeBackend)}, nil
}

func (r *Reader) ReadReservation(ctx context.Context, id string) (*Reservation, error) {
	if r == nil || r.namespace == nil {
		return nil, ErrCapability
	}
	return readReservation(ctx, r.namespace, id)
}

func (r *Reader) ReadCompletion(ctx context.Context, id string) (*CompletionRecord, error) {
	if r == nil || r.namespace == nil {
		return nil, ErrCapability
	}
	return readCompletion(ctx, r.namespace, id)
}

func (r *Reader) ReadCompleted(ctx context.Context, source executionattempt.Source, names ...string) (*CompletedExecution, error) {
	if r == nil || r.namespace == nil || r.store == nil {
		return nil, ErrCapability
	}
	return readCompleted(ctx, r, r.store, source, names...)
}
