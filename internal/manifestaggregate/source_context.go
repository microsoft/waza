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

func inspectSourceContext(ctx context.Context, backend *boundedBackend, document evidencestate.Document, inspectParent bool) (*SourceContext, error) {
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
		err = sourceContextError(err)
		result.State = classify(err)
		return result, err
	}
	if !bytes.Equal(completed.Reservation.Bytes, completed.Publication.ArtifactBytes[document.AttemptArtifact]) {
		result.State = "conflicting"
		return result, artifactmanifest.ErrConflict
	}
	result.State, result.Verification = "verified", "content-and-declared-context-only"
	result.ParentContext, err = inspectParentContext(ctx, backend, reader, completed, source, document.AttemptArtifact, inspectParent)
	return result, err
}

func sourceContextError(err error) error {
	if errors.Is(err, executionledger.ErrContext) {
		err = errors.Join(artifactmanifest.ErrConflict, err)
	}
	if errors.Is(err, executionledger.ErrInvalid) || errors.Is(err, executionattempt.ErrInvalid) ||
		errors.Is(err, evidencestate.ErrInvalid) {
		err = errors.Join(artifactmanifest.ErrCorrupt, err)
	}
	return err
}

func inspectParentContext(ctx context.Context, backend *boundedBackend, reader *executionledger.Reader,
	child *executionledger.CompletedExecution, source executionattempt.Source, artifact string, admitted bool,
) (*ParentContext, error) {
	parent := child.Reservation.Attempt.Parent
	if parent == nil {
		return nil, nil
	}
	result := &ParentContext{State: "unavailable", Verification: "unverified", Relationship: parent.Relationship}
	if parent.Relationship != "retry" {
		return result, nil
	}
	if !admitted {
		return result, ErrUnavailable
	}
	hits, unavailable := backend.hits, backend.unavailable
	state, lineage, err := evidencestate.InspectRetryParent(ctx, reader, source, artifact)
	if backend.hits != hits {
		result.State = "limit_exceeded"
		return result, errors.Join(ErrLimit, err)
	}
	if backend.unavailable != unavailable {
		err = errors.Join(ErrUnavailable, err)
	}
	if err != nil {
		err = sourceContextError(err)
		result.State = classify(err)
		if result.State == "missing" {
			result.State = "unavailable"
		}
		return result, err
	}
	if state != evidencestate.Present || lineage == nil || lineage.Relationship != evidencestate.Retry {
		return result, ErrUnavailable
	}
	// Both passes verify the same content-addressed child descriptor and exact
	// namespace bytes; an intervening parent-ID rewrite cannot supply a link.
	result.Source = &executionattempt.Source{ExecutionID: parent.ExecutionID, ManifestDigest: lineage.Manifest}
	result.State, result.Verification = "verified", "content-and-declared-context-only"
	return result, nil
}
