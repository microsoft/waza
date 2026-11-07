package manifestaggregate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/ownedsubmission"
)

// EvidenceReader shares one admitted-read budget across a response's category
// and source-context reads. Calls are serialized; create a reader per response.
// It exposes no storage writer or undecorated namespace/backend capability.
type EvidenceReader struct {
	mu                 sync.Mutex
	backend            *boundedBackend
	categoryDocument   string
	categoryReadFailed bool
}

// NewEvidenceReader performs no reads or runtime/configuration discovery.
func NewEvidenceReader(backend artifactmanifest.Backend) (*EvidenceReader, error) {
	if backend == nil {
		return nil, ErrInvalid
	}
	return &EvidenceReader{backend: &boundedBackend{Backend: backend, readBudget: &readBudget{}}}, nil
}

// InspectCategories performs one current native category-admission pass.
// Its returned facts remain separate from any subsequent source-context pass.
func (r *EvidenceReader) InspectCategories(ctx context.Context, digest string) (evidencestate.Inspection, []evidencestate.CategoryEvidence, error) {
	if r == nil || r.backend == nil {
		return evidencestate.Inspection{}, nil, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	hits, unavailable := r.backend.hits, r.backend.unavailable
	inspection, categories, err := evidencestate.InspectCategories(ctx, r.backend, digest)
	if r.backend.hits != hits {
		err = errors.Join(err, ErrLimit)
	}
	if r.backend.unavailable != unavailable {
		err = errors.Join(err, ErrUnavailable)
	}
	if err == nil && inspection.Integrity == "verified" && inspection.Document != nil {
		wire, encodeErr := json.Marshal(inspection.Document)
		err = errors.Join(err, encodeErr)
		if encodeErr == nil {
			r.categoryDocument = artifactmanifest.Hash(wire)
		}
	}
	r.categoryReadFailed = r.categoryReadFailed || err != nil
	return inspection, categories, err
}

// InspectSourceContext uses the same budget for the separately observed current
// reservation, completion and manifest reads; it does not inspect categories.
func (r *EvidenceReader) InspectSourceContext(ctx context.Context, document evidencestate.Document) (*SourceContext, error) {
	if r == nil || r.backend == nil {
		return nil, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	admitted := false
	if r.categoryDocument != "" && !r.categoryReadFailed {
		wire, err := json.Marshal(document)
		if err != nil {
			return nil, errors.Join(ErrInvalid, err)
		}
		admitted = r.categoryDocument == artifactmanifest.Hash(wire)
	}
	return inspectSourceContext(ctx, r.backend, document, admitted)
}

// InspectLifecycle admits only the completed original's fixed lifecycle capture.
// It shares the category/source budget and returns no partial metadata on error.
// Captured content is nonsnapshot and does not authenticate current liveness.
func (r *EvidenceReader) InspectLifecycle(ctx context.Context, source executionattempt.Source,
	asOf string, staleAfterMS int64,
) (ownedsubmission.LifecycleInspection, error) {
	if r == nil || r.backend == nil {
		return ownedsubmission.LifecycleInspection{}, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	reader, err := newEvidenceSourceReader(r.backend)
	if err != nil {
		return ownedsubmission.LifecycleInspection{}, err
	}
	hits, unavailable := r.backend.hits, r.backend.unavailable
	result, err := ownedsubmission.InspectLifecycle(ctx, reader, source, asOf, staleAfterMS)
	if r.backend.hits != hits {
		err = errors.Join(err, ErrLimit)
	}
	if r.backend.unavailable != unavailable {
		err = errors.Join(err, ErrUnavailable)
	}
	if err != nil {
		return ownedsubmission.LifecycleInspection{}, err
	}
	return result, nil
}
