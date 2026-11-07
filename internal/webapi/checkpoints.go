package webapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/projectconfig"
)

// CheckpointInspection describes current blob admission, not an owned restore.
// Unknown compatibility and progress remain explicit null/unavailable values.
type CheckpointInspection struct {
	CheckpointSHA256 string                          `json:"checkpoint_sha256"`
	Availability     string                          `json:"availability"`
	Verification     string                          `json:"verification"`
	Checkpoint       *executioncheckpoint.Checkpoint `json:"checkpoint,omitempty"`
	Compatibility    string                          `json:"compatibility_result"`
	RestoredUnits    *int                            `json:"restored_units"`
	RemainingWork    *int                            `json:"remaining_work"`
	Diagnostics      []artifactmanifest.Diagnostic   `json:"diagnostics"`
}

type checkpointBlobInspector struct {
	open func() (artifactmanifest.Backend, func() error, error)
}

func (i checkpointBlobInspector) inspect(ctx context.Context, digest string) (result CheckpointInspection, resultErr error) {
	result = CheckpointInspection{
		CheckpointSHA256: digest, Availability: "unavailable",
		Verification: "canonical-declaration-only", Compatibility: "unavailable",
		Diagnostics: []artifactmanifest.Diagnostic{},
	}
	diagnostic := func(code, message string) {
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{Code: code, Message: message})
	}
	if err := executioncheckpoint.ValidateDigest(digest); err != nil {
		diagnostic("invalid_digest", "Provide a full lowercase SHA-256 checkpoint blob digest.")
		return result, err
	}
	if err := ctx.Err(); err != nil {
		diagnostic("inspection_canceled", "Checkpoint inspection was canceled; no source was admitted.")
		return result, err
	}
	backend, closeBackend, err := i.open()
	if err != nil {
		diagnostic("storage_error", "Checkpoint storage is unavailable. Check operator diagnostics; no legacy results were substituted.")
		return result, err
	}
	defer func() {
		if err := closeBackend(); err != nil {
			resultErr = errors.Join(resultErr, err)
			result.Availability, result.Checkpoint = "unavailable", nil
			diagnostic("storage_error", "Checkpoint storage could not be closed; inspection did not complete successfully.")
		}
	}()
	checkpoint, err := executioncheckpoint.Load(ctx, backend, digest)
	if err != nil {
		switch {
		case errors.Is(err, artifactmanifest.ErrMissing):
			diagnostic("missing_object", "The checkpoint blob is missing. Select its original immutable store and digest.")
		case errors.Is(err, artifactmanifest.ErrCorrupt):
			diagnostic("corrupt_object", "Checkpoint hash, schema or canonical bytes failed admission. Preserve the source; do not normalize or overwrite it.")
		default:
			diagnostic("storage_error", "Checkpoint inspection failed. Check operator diagnostics and retry without replacing evidence.")
		}
		return result, err
	}
	result.Availability, result.Checkpoint = "blob-verified", checkpoint
	diagnostic("source_not_authenticated", "Blob admission does not authenticate the original attempt or establish input compatibility and restore progress.")
	return result, nil
}

func registerCheckpointRoutes(mux *http.ServeMux, inspector checkpointBlobInspector) {
	mux.HandleFunc("GET /api/v1/checkpoints/{digest}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result, err := inspector.inspect(ctx, r.PathValue("digest"))
		status := http.StatusOK
		if err != nil {
			switch {
			case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, executioncheckpoint.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, artifactmanifest.ErrMissing):
				status = http.StatusNotFound
			default:
				status = http.StatusServiceUnavailable
			}
			slog.Error("checkpoint blob inspection failed", "error", err)
		}
		writeJSON(w, status, result)
	})
}

// RegisterCheckpointRoutes reads only caller-known digests in the configured
// immutable store. It cannot discover checkpoints or execute/restore work.
func RegisterCheckpointRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	registerCheckpointRoutes(mux, checkpointBlobInspector{open: checkpointBackendFor(resultsDir, config)})
}

func checkpointBackendFor(resultsDir string, config *projectconfig.StorageConfig) func() (artifactmanifest.Backend, func() error, error) {
	provider := manifestInspectorFor(resultsDir, config)
	return func() (artifactmanifest.Backend, func() error, error) {
		if config != nil && config.Enabled && config.Provider != "" && config.Provider != "local" && config.Provider != "azure-blob" {
			return nil, nil, fmt.Errorf("checkpoint inspection does not support the configured active provider")
		}
		return provider.openBackend()
	}
}
