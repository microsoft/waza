package webserver

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/spboyer/waza/web"
)

// registerRoutes sets up API and SPA routes on the given mux.
func registerRoutes(mux *http.ServeMux, cfg Config) error {
	// API routes
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("/api/", handleAPIPlaceholder)

	// SPA static files with HTML5 history API fallback
	handler, err := spaHandler()
	if err != nil {
		return fmt.Errorf("failed to initialize SPA handler: %w", err)
	}
	mux.Handle("/", handler)
	return nil
}

// handleHealth returns a simple health check response.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
}

// handleAPIPlaceholder returns 501 for unimplemented API endpoints.
func handleAPIPlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(map[string]string{"error": "not implemented"}) //nolint:errcheck
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
