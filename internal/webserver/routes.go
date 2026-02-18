package webserver

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/spboyer/waza/web"
)

// registerRoutes sets up API and SPA routes on the given mux.
func registerRoutes(mux *http.ServeMux, cfg Config) {
	// API routes
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("/api/", handleAPIPlaceholder)

	// SPA static files with HTML5 history API fallback
	mux.Handle("/", spaHandler())
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
// Non-existent paths that don't look like file requests are served index.html
// to support client-side routing (HTML5 history API fallback).
func spaHandler() http.Handler {
	distFS, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		panic("failed to create sub filesystem for web/dist: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try to serve the file directly.
		if path != "/" {
			// Check if the file exists in the embedded FS.
			cleanPath := strings.TrimPrefix(path, "/")
			if f, err := distFS.Open(cleanPath); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Fallback: serve index.html for SPA routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
