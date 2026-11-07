package webapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/evidenceview"
	"github.com/microsoft/waza/internal/projectconfig"
)

type evidenceReader func(context.Context, string) (evidencestate.Inspection, error)

func registerEvidenceRoutes(mux *http.ServeMux, read evidenceReader) {
	for _, route := range []struct {
		pattern string
		format  string
	}{
		{"GET /api/v1/evidence/{digest}", ""},
		{"GET /api/v1/evidence/{digest}/document", "json"},
		{"GET /api/v1/evidence/{digest}/csv", "csv"},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			digest := r.PathValue("digest")
			result, err := read(ctx, digest)
			if err == nil && (result.Integrity != "verified" || result.Document == nil) {
				err = artifactmanifest.ErrCorrupt
				result.Integrity = "failed"
				result.Document = nil
				result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
					Code: "invalid_evidence_receipt", Message: "The evidence receipt did not establish current source integrity.",
				})
			}
			if err != nil || route.format == "" {
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
					slog.Error("derived evidence inspection failed", "error", err)
				}
				writeJSON(w, status, result)
				return
			}
			if route.format == "json" {
				data, encodeErr := evidencestate.Encode(*result.Document)
				if encodeErr != nil {
					slog.Error("encoding verified evidence download", "error", encodeErr)
					http.Error(w, "Evidence download could not be encoded.", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Disposition", `attachment; filename="execution-evidence-`+digest+`.json"`)
				if _, err := w.Write(data); err != nil {
					slog.Error("writing evidence JSON download", "error", err)
				}
				return
			}
			var data bytes.Buffer
			writer := csv.NewWriter(&data)
			rows := [][]string{{"category", "state", "source", "context", "observed_count", "reason", "artifacts", "origin_manifest", "execution_id", "limit_observed", "limit_total"}}
			for _, record := range result.Document.Categories {
				count, observed, total, executionID := "", "", "", ""
				if record.ObservedCount != nil {
					count = strconv.FormatInt(*record.ObservedCount, 10)
				}
				if record.Limit != nil {
					observed = strconv.FormatInt(record.Limit.Observed, 10)
					if record.Limit.Total != nil {
						total = strconv.FormatInt(*record.Limit.Total, 10)
					}
				}
				if result.Document.ExecutionSource != nil {
					executionID = result.Document.ExecutionSource.ExecutionID
				}
				rows = append(rows, []string{
					string(record.Category), string(record.State), string(record.Source), string(record.Context),
					count, string(record.Reason), strings.Join(record.Artifacts, ";"),
					result.Document.OriginManifest, executionID, observed, total,
				})
			}
			if err := writer.WriteAll(rows); err != nil {
				slog.Error("encoding evidence CSV download", "error", err)
				http.Error(w, "Evidence download could not be encoded.", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="execution-evidence-`+digest+`.csv"`)
			if _, err := w.Write(data.Bytes()); err != nil {
				slog.Error("writing evidence CSV download", "error", err)
			}
		})
	}
}

// RegisterEvidenceRoutesWithBackend exposes only verified metadata and downloads,
// not a writable route or source payload endpoint.
func RegisterEvidenceRoutesWithBackend(mux *http.ServeMux, backend artifactmanifest.Backend) {
	registerEvidenceRoutes(mux, func(ctx context.Context, digest string) (evidencestate.Inspection, error) {
		return evidencestate.Inspect(ctx, backend, digest)
	})
	registerEvidenceCategoryRoute(mux, func(ctx context.Context, digest string) (evidenceview.Categories, error) {
		return evidenceview.InspectCategories(ctx, backend, digest)
	})
}

func evidenceUnavailable(digest string, err error) (evidencestate.Inspection, error) {
	return evidencestate.Inspection{
		EvidenceSHA256: digest, Publication: "derived", Integrity: "failed",
		Verification: "content-and-declared-context-only", Sources: []artifactmanifest.Receipt{},
		Diagnostics: []artifactmanifest.Diagnostic{{Code: "storage_error", Message: "Evidence is unavailable; no legacy result or synthetic success was substituted."}},
	}, err
}

func RegisterEvidenceRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	read := func(ctx context.Context, digest string, allCategories bool) (result evidenceview.Categories, resultErr error) {
		if err := evidencestate.ValidateDigest(digest); err != nil {
			if allCategories {
				return evidenceview.InspectCategories(ctx, nil, digest)
			}
			result, err := evidencestate.Inspect(ctx, nil, digest)
			return evidenceview.Categories{Inspection: result}, err
		}
		unavailable := func(err error) (evidenceview.Categories, error) {
			result, resultErr := evidenceUnavailable(digest, err)
			if allCategories {
				_, missing, missingErr := evidencestate.InspectCategories(ctx, nil, digest)
				return evidenceview.Categories{Inspection: result, Categories: missing}, errors.Join(resultErr, missingErr)
			}
			return evidenceview.Categories{Inspection: result}, resultErr
		}
		directory, azureURL := filepath.Join(resultsDir, ".waza-artifacts"), ""
		if config != nil && config.Enabled && config.Provider == "azure-blob" {
			directory = ""
			azureURL = "https://" + config.AccountName + ".blob.core.windows.net/" + config.ContainerName
		} else if config != nil && config.Enabled && config.Provider != "" && config.Provider != "local" {
			return unavailable(errors.New("unsupported configured evidence storage provider"))
		}
		backend, closeStore, err := artifactmanifest.OpenBackend(directory, azureURL)
		if err != nil {
			return unavailable(err)
		}
		defer func() {
			if err := closeStore(); err != nil {
				resultErr = errors.Join(resultErr, err)
				result.Document = nil
				result.Integrity = "failed"
				result.SourceContext = nil
				result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{Code: "storage_error", Message: "Evidence inspection did not close successfully."})
				if allCategories {
					_, missing, missingErr := evidencestate.InspectCategories(ctx, nil, digest)
					result.Categories = missing
					resultErr = errors.Join(resultErr, missingErr)
				}
			}
		}()
		if allCategories {
			return evidenceview.InspectCategories(ctx, backend, digest)
		}
		result.Inspection, err = evidencestate.Inspect(ctx, backend, digest)
		return result, err
	}
	registerEvidenceRoutes(mux, func(ctx context.Context, digest string) (evidencestate.Inspection, error) {
		result, err := read(ctx, digest, false)
		return result.Inspection, err
	})
	registerEvidenceCategoryRoute(mux, func(ctx context.Context, digest string) (evidenceview.Categories, error) {
		return read(ctx, digest, true)
	})
}
