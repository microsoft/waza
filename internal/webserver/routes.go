package webserver

import (
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/spboyer/waza/internal/storage"
	"github.com/spboyer/waza/internal/webapi"
	"github.com/spboyer/waza/web"
)

// registerRoutes sets up API and SPA routes on the given mux.
func registerRoutes(mux *http.ServeMux, cfg Config) error {
	var runStore webapi.RunStore
	var storageCfg *webapi.StorageConfig

	// If storage is configured, use ResultStore adapter.
	if cfg.StorageConfig != nil && cfg.StorageConfig.Enabled {
		resultStore, err := storage.NewStore(cfg.StorageConfig, cfg.ResultsDir)
		if err != nil {
			// Log warning but fallback to local FileStore.
			if cfg.Logger != nil {
				cfg.Logger.Warn("failed to create storage backend, falling back to local",
					"error", err,
					"provider", cfg.StorageConfig.Provider)
			}
			runStore = webapi.NewFileStore(cfg.ResultsDir)
			storageCfg = &webapi.StorageConfig{
				Configured: false,
			}
		} else {
			// Successfully created storage-backed store.
			source := cfg.StorageConfig.Provider
			if source == "" {
				source = "local"
			}
			runStore = webapi.NewStorageAdapter(resultStore, source)
			storageCfg = &webapi.StorageConfig{
				Configured: true,
				Provider:   cfg.StorageConfig.Provider,
				Account:    cfg.StorageConfig.AccountName,
			}
			if cfg.Logger != nil {
				cfg.Logger.Info("using storage backend",
					"provider", cfg.StorageConfig.Provider,
					"account", cfg.StorageConfig.AccountName,
					"container", cfg.StorageConfig.ContainerName)
			}
		}
	} else {
		// No storage configured, use local FileStore.
		runStore = webapi.NewFileStore(cfg.ResultsDir)
		storageCfg = &webapi.StorageConfig{
			Configured: false,
		}
	}

	// Register API routes with storage configuration.
	webapi.RegisterRoutesWithStorage(mux, runStore, storageCfg)

	// SPA static files with HTML5 history API fallback
	handler, err := spaHandler()
	if err != nil {
		return fmt.Errorf("failed to initialize SPA handler: %w", err)
	}
	mux.Handle("/", handler)
	return nil
}

// spaHandler returns an http.Handler that serves the embedded SPA assets.
// Non-existent paths are served index.html to support client-side routing
// (HTML5 history API fallback).
func spaHandler() (http.Handler, error) {
	distFS, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("failed to create sub filesystem for web/dist: %w", err)
	}

	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try to serve the file directly.
		if path != "/" {
			// Check if the file exists in the embedded FS.
			cleanPath := strings.TrimPrefix(path, "/")
			if f, err := distFS.Open(cleanPath); err == nil {
				f.Close() //nolint:errcheck
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Fallback: serve index.html for SPA routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}), nil
}
