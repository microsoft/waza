package webapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
)

type evidenceCategoryReader func(context.Context, string) (evidencestate.Inspection, []evidencestate.CategoryEvidence, error)

func registerEvidenceCategoryRoute(mux *http.ServeMux, read evidenceCategoryReader) {
	mux.HandleFunc("GET /api/v1/evidence/{digest}/categories", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		inspection, categories, err := read(ctx, r.PathValue("digest"))
		if err == nil && (inspection.Integrity != "verified" || inspection.Document == nil || len(categories) != 6) {
			err = artifactmanifest.ErrCorrupt
			inspection.Integrity, inspection.Document = "failed", nil
			_, missing, missingErr := evidencestate.InspectCategories(ctx, nil, r.PathValue("digest"))
			categories = missing
			err = errors.Join(err, missingErr)
			inspection.Diagnostics = append(inspection.Diagnostics, artifactmanifest.Diagnostic{
				Code: "invalid_evidence_receipt", Message: "The category receipt did not establish current source integrity.",
			})
		}
		status := http.StatusOK
		switch {
		case err == nil:
		case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict):
			status = http.StatusConflict
		case errors.Is(err, evidencestate.ErrInvalid), errors.Is(err, artifactmanifest.ErrInvalid):
			status = http.StatusBadRequest
		case errors.Is(err, artifactmanifest.ErrMissing):
			status = http.StatusNotFound
		default:
			status = http.StatusServiceUnavailable
			slog.Error("recorded evidence category inspection failed", "error", err)
		}
		if err != nil && inspection.Document != nil {
			inspection.Diagnostics = append(inspection.Diagnostics, artifactmanifest.Diagnostic{
				Code:    "category_admission_failed",
				Message: "Some category claims were rejected; inspect their individual states. No assurance acceptance was established.",
			})
		}
		writeJSON(w, status, struct {
			evidencestate.Inspection
			Categories []evidencestate.CategoryEvidence `json:"categories"`
		}{inspection, categories})
	})
}
