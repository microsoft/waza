package webapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/projectconfig"
)

type AggregateReader interface {
	Aggregate(context.Context, manifestaggregate.Request) (manifestaggregate.Report, error)
}

func RegisterAggregateRoutesWithReader(mux *http.ServeMux, reader AggregateReader) {
	mux.HandleFunc("POST /api/v1/aggregates", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Aggregate request exceeds 16 KiB or could not be read."})
			return
		}
		request, err := manifestaggregate.ParseRequest(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Provide an explicit manifests array containing 1..128 lowercase SHA-256 digests."})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		report, err := reader.Aggregate(ctx, request)
		status := http.StatusOK
		if err != nil {
			status = http.StatusConflict
			if errors.Is(err, manifestaggregate.ErrUnavailable) || !errors.Is(err, manifestaggregate.ErrIncomplete) {
				status = http.StatusServiceUnavailable
			}
			slog.Error("completed manifest aggregation failed", "error", err)
		}
		data, encodeErr := manifestaggregate.Encode(report)
		if encodeErr != nil {
			slog.Error("manifest aggregate encoding failed", "error", encodeErr)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Aggregate report could not be emitted; no previous result was substituted."})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := w.Write(data); err != nil {
			slog.Error("manifest aggregate response failed", "error", err)
		}
	})
}

type configuredAggregateReader struct {
	directory string
	config    *projectconfig.StorageConfig
}

func (i configuredAggregateReader) Aggregate(ctx context.Context, request manifestaggregate.Request) (manifestaggregate.Report, error) {
	// The caller validates before provider construction; retain that invariant
	// for direct callers as well.
	if err := request.Validate(); err != nil {
		return manifestaggregate.Report{}, err
	}
	if i.config != nil && i.config.Enabled && i.config.Provider == "azure-blob" {
		return manifestaggregate.Report{}, errors.New("aggregate API requires the explicit local immutable store; configured Azure aggregate reads are not supported")
	}
	backend, err := artifactmanifest.OpenLocal(i.directory)
	if err != nil {
		return manifestaggregate.Report{}, err
	}
	report, aggregateErr := manifestaggregate.Aggregate(ctx, backend, request)
	closeErr := backend.Close()
	if closeErr != nil {
		report.Completion = "incomplete"
		report.Diagnostics = append(report.Diagnostics, artifactmanifest.Diagnostic{
			Code: "storage_close_failed", Message: "Store close failed; aggregation did not complete successfully.",
		})
	}
	return report, errors.Join(aggregateErr, closeErr)
}

func RegisterAggregateRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	RegisterAggregateRoutesWithReader(mux, configuredAggregateReader{
		directory: filepath.Join(resultsDir, ".waza-artifacts"), config: config,
	})
}
