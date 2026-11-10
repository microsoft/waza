package webapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
)

// AggregateSources opens explicitly configured read-only capabilities per
// request. Providers must not construct runtimes or discover worker-local state.
type AggregateSources func() (manifestaggregate.Sources, func() error, error)

func RegisterAggregateRoutesWithSources(mux *http.ServeMux, open AggregateSources) {
	mux.HandleFunc("POST /api/v1/aggregates", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, artifactmanifest.MaxManifestBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_aggregate_request"})
			return
		}
		inputs, err := manifestaggregate.ParseInputs(data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_aggregate_request"})
			return
		}
		sources, closeSources, err := open()
		if err != nil {
			slog.Error("aggregate store unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "aggregate_store_unavailable"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		report, buildErr := manifestaggregate.Build(ctx, sources, inputs, manifestaggregate.DefaultLimits())
		closeErr := closeSources()
		if report == nil || closeErr != nil {
			slog.Error("aggregate evaluation unavailable", "error", errors.Join(buildErr, closeErr))
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "aggregate_evaluation_unavailable"})
			return
		}
		wire, encodeErr := manifestaggregate.Encode(*report)
		if encodeErr != nil {
			slog.Error("aggregate encoding failed", "error", encodeErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "aggregate_encoding_failed"})
			return
		}
		status := http.StatusOK
		if buildErr != nil {
			status = http.StatusConflict
			slog.Error("aggregate admission incomplete", "error", buildErr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		if _, err := w.Write(wire); err != nil {
			slog.Error("aggregate response write failed", "error", err)
		}
	})
}
