package webapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/projectconfig"
)

type LifecycleInspector interface {
	Inspect(context.Context, []byte) (executionlifecycle.Snapshot, error)
}

func RegisterLifecycleRoutesWithInspector(mux *http.ServeMux, inspector LifecycleInspector) {
	mux.HandleFunc("POST /api/v1/lifecycle/inspect", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		fail := func(status int, message string) {
			if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				slog.Error("lifecycle inspection write deadline unavailable", "error", err)
				panic(http.ErrAbortHandler)
			}
			writeError(w, status, message)
		}
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			fail(http.StatusInternalServerError, "Lifecycle input deadline is unavailable.")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			fail(http.StatusUnsupportedMediaType, "Lifecycle inspection requires Content-Type: application/json.")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, executionlifecycle.MaxBytes))
		if err != nil {
			status := http.StatusBadRequest
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				status = http.StatusRequestEntityTooLarge
			}
			fail(status, "Lifecycle inspection context could not be read within its bounds.")
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			fail(http.StatusInternalServerError, "Lifecycle input deadline could not be reset.")
			return
		}
		if _, err := executionlifecycle.ParseInspection(data); err != nil {
			fail(http.StatusBadRequest, "Invalid lifecycle inspection context; provide explicit original admission and ordered lowercase digests.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		result, err := inspector.Inspect(ctx, data)
		if err != nil {
			status := http.StatusServiceUnavailable
			switch {
			case errors.Is(err, executionlifecycle.ErrContext), errors.Is(err, executionlifecycle.ErrTransition),
				errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, executionlifecycle.ErrInvalid), errors.Is(err, artifactmanifest.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, artifactmanifest.ErrMissing):
				status = http.StatusNotFound
			}
			slog.Error("lifecycle inspection unavailable", "error", err)
			fail(status, "Lifecycle declarations could not be inspected. No previous projection, verified worker state or progress is available.")
			return
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			slog.Error("lifecycle inspection write deadline unavailable", "error", err)
			panic(http.ErrAbortHandler)
		}
		writeJSON(w, http.StatusOK, result)
	})
}

type configuredLifecycleInspector struct {
	directory string
	config    *projectconfig.StorageConfig
}

func (i configuredLifecycleInspector) Inspect(ctx context.Context, data []byte) (executionlifecycle.Snapshot, error) {
	if _, err := executionlifecycle.ParseInspection(data); err != nil {
		return executionlifecycle.Snapshot{}, err
	}
	backend, closeBackend, err := i.openBackend()
	if err != nil {
		return executionlifecycle.Snapshot{}, err
	}
	result, err := executionlifecycle.Inspect(ctx, backend, data)
	if err := errors.Join(err, closeBackend(), ctx.Err()); err != nil {
		return executionlifecycle.Snapshot{}, err
	}
	return result, nil
}

func (i configuredLifecycleInspector) openBackend() (artifactmanifest.Backend, func() error, error) {
	azureURL := ""
	if i.config != nil && i.config.Enabled && i.config.Provider == "azure-blob" {
		azureURL = "https://" + i.config.AccountName + ".blob.core.windows.net/" + i.config.ContainerName
	}
	return artifactmanifest.OpenBackend(i.directory, azureURL)
}

func RegisterLifecycleRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	inspector := configuredLifecycleInspector{
		directory: filepath.Join(resultsDir, ".waza-artifacts"), config: config,
	}
	RegisterLifecycleRoutesWithInspector(mux, inspector)
	RegisterLifecycleSourceRoutesWithInspector(mux, inspector)
}
