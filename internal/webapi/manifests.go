package webapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/projectconfig"
)

type ManifestInspector interface {
	Inspect(context.Context, string) (artifactmanifest.Receipt, error)
}

type ManifestCatalog interface {
	Catalog(context.Context) (artifactmanifest.Catalog, error)
}

func RegisterManifestRoutesWithInspector(mux *http.ServeMux, inspector ManifestInspector) {
	if catalog, ok := inspector.(ManifestCatalog); ok {
		mux.HandleFunc("GET /api/v1/manifests", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			result, err := catalog.Catalog(ctx)
			status := http.StatusOK
			if err != nil {
				status = http.StatusServiceUnavailable
				slog.Error("immutable manifest discovery failed", "error", err)
			}
			writeJSON(w, status, result)
		})
	}
	mux.HandleFunc("GET /api/v1/manifests/{digest}", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		receipt, err := inspector.Inspect(ctx, r.PathValue("digest"))
		status := http.StatusOK
		if err != nil {
			// Details stay in operator logs, never in serialized Lab diagnostics.
			slog.Error("immutable manifest inspection failed", "error", err)
			switch {
			case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrConflict):
				status = http.StatusConflict
			case errors.Is(err, artifactmanifest.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, artifactmanifest.ErrMissing):
				status = http.StatusNotFound
			default:
				status = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, status, receipt)
	})
}

type configuredManifestInspector struct {
	directory string
	config    *projectconfig.StorageConfig
}

func (i configuredManifestInspector) open() (*artifactmanifest.Store, func() error, error) {
	var azureURL string
	if i.config != nil && i.config.Enabled && i.config.Provider == "azure-blob" {
		azureURL = "https://" + i.config.AccountName + ".blob.core.windows.net/" + i.config.ContainerName
	}
	backend, closeBackend, err := artifactmanifest.OpenBackend(i.directory, azureURL)
	if err != nil {
		return nil, nil, err
	}
	return artifactmanifest.NewStore(backend), closeBackend, nil
}

func (i configuredManifestInspector) Catalog(ctx context.Context) (result artifactmanifest.Catalog, resultErr error) {
	store, closeStore, err := i.open()
	if err != nil {
		return artifactmanifest.Catalog{ManifestSHA256: []string{}, Verification: "unverified-pointers-only",
			Diagnostics: []artifactmanifest.Diagnostic{{Code: "storage_error", Message: "Immutable store is unavailable; no legacy results were substituted."}}}, err
	}
	defer func() {
		if err := closeStore(); err != nil {
			resultErr = errors.Join(resultErr, err)
			result.ManifestSHA256 = []string{}
			result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{Code: "storage_error", Message: "Manifest discovery did not complete successfully."})
		}
	}()
	return store.Catalog(ctx)
}

func (i configuredManifestInspector) Inspect(ctx context.Context, digest string) (receipt artifactmanifest.Receipt, resultErr error) {
	unavailable := func(err error) (artifactmanifest.Receipt, error) {
		return artifactmanifest.Receipt{
			ManifestSHA256: digest, Publication: "unfinalized", Integrity: "unverified",
			Verification: "content-and-declared-context-only", Artifacts: []artifactmanifest.ArtifactState{},
			Diagnostics: []artifactmanifest.Diagnostic{{Code: "storage_error", Message: "Immutable store is unavailable. Select an existing private store; no legacy result was substituted."}},
		}, err
	}
	// Validate the digest before provider construction or credentials.
	if len(digest) != 64 {
		r, _ := unavailable(artifactmanifest.ErrInvalid)
		r.Diagnostics = []artifactmanifest.Diagnostic{{Code: "invalid_declaration", Message: "Provide a full lowercase SHA-256 manifest digest."}}
		return r, artifactmanifest.ErrInvalid
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			r, _ := unavailable(artifactmanifest.ErrInvalid)
			r.Diagnostics = []artifactmanifest.Diagnostic{{Code: "invalid_declaration", Message: "Provide a full lowercase SHA-256 manifest digest."}}
			return r, artifactmanifest.ErrInvalid
		}
	}
	store, closeStore, err := i.open()
	if err != nil {
		return unavailable(err)
	}
	defer func() {
		if err := closeStore(); err != nil {
			resultErr = errors.Join(resultErr, err)
			receipt.Integrity = "unverified"
			receipt.Diagnostics = append(receipt.Diagnostics, artifactmanifest.Diagnostic{Code: "storage_error", Message: "Immutable store could not be closed; inspection did not complete successfully."})
		}
	}()
	return store.Inspect(ctx, digest)
}

// The dashboard's configured results directory selects a separate opt-in store.
// Query input can select only a digest, never a host path or provider URL.
func RegisterManifestRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	RegisterManifestRoutesWithInspector(mux, configuredManifestInspector{
		directory: filepath.Join(resultsDir, ".waza-artifacts"), config: config,
	})
}
