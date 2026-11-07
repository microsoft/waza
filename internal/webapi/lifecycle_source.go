package webapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/ownedsubmission"
)

type LifecycleSourceInspector interface {
	InspectLifecycle(context.Context, executionattempt.Source, string, int64) (ownedsubmission.LifecycleInspection, error)
}

func RegisterLifecycleSourceRoutesWithInspector(mux *http.ServeMux, inspector LifecycleSourceInspector) {
	mux.HandleFunc("POST /api/v1/lifecycle/source", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		fail := func(status int, message string) {
			if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				slog.Error("lifecycle source write deadline unavailable", "error", err)
				panic(http.ErrAbortHandler)
			}
			writeError(w, status, message)
		}
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			fail(http.StatusInternalServerError, "Lifecycle source input deadline is unavailable.")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			fail(http.StatusUnsupportedMediaType, "Lifecycle source inspection requires Content-Type: application/json.")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, executionlifecycle.MaxBytes))
		if err != nil {
			status := http.StatusBadRequest
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				status = http.StatusRequestEntityTooLarge
			}
			fail(status, "Lifecycle source selection could not be read within its bounds.")
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			fail(http.StatusInternalServerError, "Lifecycle source input deadline could not be reset.")
			return
		}
		request, err := ownedsubmission.ParseLifecycleSourceRequest(data)
		if err != nil {
			fail(http.StatusBadRequest, "Invalid original lifecycle source selection; provide original execution/manifest and explicit positive stale_after_ms.")
			return
		}
		if inspector == nil {
			fail(http.StatusServiceUnavailable, "Original lifecycle source inspection is unavailable.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result, err := inspector.InspectLifecycle(ctx, request.Source, request.AsOf, request.StaleAfterMS)
		if err := errors.Join(err, ctx.Err()); err != nil {
			status := http.StatusServiceUnavailable
			switch {
			case errors.Is(err, manifestaggregate.ErrLimit), errors.Is(err, manifestaggregate.ErrUnavailable):
				status = http.StatusServiceUnavailable
			case errors.Is(err, artifactmanifest.ErrMissing):
				status = http.StatusNotFound
			case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict),
				errors.Is(err, artifactmanifest.ErrInvalid), errors.Is(err, executionledger.ErrContext),
				errors.Is(err, executionledger.ErrInvalid), errors.Is(err, executionattempt.ErrInvalid),
				errors.Is(err, executionlifecycle.ErrContext), errors.Is(err, executionlifecycle.ErrTransition),
				errors.Is(err, executionlifecycle.ErrInvalid), errors.Is(err, execution.ErrRuntimeObservation):
				status = http.StatusConflict
			}

			slog.Error("original lifecycle source unavailable", "error", err)
			fail(status, "Original lifecycle source could not be verified. No previous, partial or private-runtime fallback is available.")
			return
		}
		if result.Source != request.Source || result.Snapshot.AsOf != request.AsOf {
			fail(http.StatusInternalServerError, "Lifecycle source response contradicts the requested original source.")
			return
		}
		output, err := ownedsubmission.EncodeLifecycleInspection(result)
		if err != nil {
			slog.Error("original lifecycle source response invalid", "error", err)
			fail(http.StatusInternalServerError, "Lifecycle source response could not be validated.")
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			slog.Error("lifecycle source write deadline unavailable", "error", err)
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(append(output, '\n')); err != nil {
			slog.Error("writing original lifecycle source", "error", err)
			panic(http.ErrAbortHandler)
		}
	})
}

func (i configuredLifecycleInspector) InspectLifecycle(ctx context.Context, source executionattempt.Source,
	asOf string, staleAfterMS int64,
) (ownedsubmission.LifecycleInspection, error) {
	backend, closeBackend, err := i.openBackend()
	if err != nil {
		return ownedsubmission.LifecycleInspection{}, err
	}
	reader, err := manifestaggregate.NewEvidenceReader(backend)
	var result ownedsubmission.LifecycleInspection
	if err == nil {
		result, err = reader.InspectLifecycle(ctx, source, asOf, staleAfterMS)
	}
	if err := errors.Join(err, closeBackend(), ctx.Err()); err != nil {
		return ownedsubmission.LifecycleInspection{}, err
	}
	return result, nil
}
