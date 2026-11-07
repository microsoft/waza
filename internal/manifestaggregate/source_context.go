package manifestaggregate

import (
	"bytes"
	"context"
	"errors"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

func inspectSourceContext(ctx context.Context, backend *boundedBackend, document evidencestate.Document) (*SourceContext, error) {
	result := &SourceContext{State: "unavailable", Verification: "unverified"}
	if document.ExecutionSource == nil {
		return result, nil
	}
	source := *document.ExecutionSource
	result.Source = &source
	// Only the originally supplied concrete provider admits a namespace.
	// Both read paths are decorated before I/O; no writer is passed to Reader.
	namespace, err := artifactmanifest.NewNamespace(backend.Backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		return result, errors.Join(ErrUnavailable, err)
	}
	namespaceReader, err := namespace.Reader(backend.decorate)
	if err != nil {
		return result, errors.Join(ErrUnavailable, err)
	}
	reader, err := executionledger.NewReader(namespaceReader, backend)
	if err != nil {
		return result, errors.Join(ErrUnavailable, err)
	}
	hits, unavailable := backend.hits, backend.unavailable
	completed, err := reader.ReadCompleted(ctx, source, document.AttemptArtifact)
	if backend.hits != hits {
		result.State = "limit_exceeded"
		return result, errors.Join(ErrLimit, err)
	}
	if backend.unavailable != unavailable {
		err = errors.Join(ErrUnavailable, err)
	}
	if err != nil {
		if errors.Is(err, executionledger.ErrContext) {
			err = errors.Join(artifactmanifest.ErrConflict, err)
		}
		if errors.Is(err, executionledger.ErrInvalid) || errors.Is(err, executionattempt.ErrInvalid) {
			err = errors.Join(artifactmanifest.ErrCorrupt, err)
		}
		result.State = classify(err)
		return result, err
	}
	if !bytes.Equal(completed.Reservation.Bytes, completed.Publication.ArtifactBytes[document.AttemptArtifact]) {
		result.State = "conflicting"
		return result, artifactmanifest.ErrConflict
	}
	result.State, result.Verification = "verified", "content-and-declared-context-only"
	return result, nil
}
