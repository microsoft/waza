package webapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Version is set at build time or defaults to dev.
var Version = "0.4.0-alpha.1"

var runEventsPollInterval = 250 * time.Millisecond

// Handlers holds the HTTP handler methods for the web API.
type Handlers struct {
	store         RunStore
	storageConfig *StorageConfig
}

// StorageConfig holds storage configuration for the status endpoint.
type StorageConfig struct {
	Configured bool
	Provider   string
	Account    string
}

// NewHandlers creates a new Handlers with the given store.
func NewHandlers(store RunStore) *Handlers {
	return &Handlers{store: store}
}

// NewHandlersWithStorage creates a new Handlers with storage configuration.
func NewHandlersWithStorage(store RunStore, cfg *StorageConfig) *Handlers {
	return &Handlers{
		store:         store,
		storageConfig: cfg,
	}
}

// HandleHealth returns a simple health check response.
func (h *Handlers) HandleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:  "ok",
		Version: Version,
	})
}

// HandleSummary returns aggregate KPI metrics across all runs.
func (h *Handlers) HandleSummary(w http.ResponseWriter, _ *http.Request) {
	summary, err := h.store.Summary()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// HandleRuns returns a list of all runs, with optional sort/order query params.
func (h *Handlers) HandleRuns(w http.ResponseWriter, r *http.Request) {
	sortField := r.URL.Query().Get("sort")
	order := r.URL.Query().Get("order")

	runs, err := h.store.ListRuns(sortField, order)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// HandleRunDetail returns full run detail with per-task results.
func (h *Handlers) HandleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		// Fallback: extract from URL path for compatibility.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
		if len(parts) > 0 {
			id = parts[0]
		}
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "run id is required")
		return
	}

	detail, err := h.store.GetRun(id)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// HandleRunEvents streams replayable Server-Sent Events for a run.
func (h *Handlers) HandleRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = runIDFromEventsPath(r.URL.Path)
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "run id is required")
		return
	}

	lastID, err := lastEventID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	detail, err := h.store.GetRun(id)
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			writeError(w, http.StatusNotFound, "run not found")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeSSEHeaders(w)
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return
	}
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	nextPoll := time.NewTimer(0)
	defer nextPoll.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-nextPoll.C:
		}

		events := filterEventsAfter(runEventsFromDetail(detail), lastID)
		for _, event := range events {
			if err := writeRunSSEEvent(w, event); err != nil {
				return
			}
			lastID = event.Sequence
			if flusher != nil {
				flusher.Flush()
			}
			if event.Type == RunEventCompleted || event.Type == RunEventFailed {
				return
			}
		}

		nextPoll.Reset(runEventsPollInterval)
		select {
		case <-r.Context().Done():
			return
		case <-nextPoll.C:
		}

		detail, err = h.getRunForEvents(id)
		if err != nil {
			return
		}
		nextPoll.Reset(0)
	}
}

// HandleLatestRunEvents preserves the legacy /api/events stream by replaying
// the newest run in the same SSE format as the v1 per-run endpoint.
func (h *Handlers) HandleLatestRunEvents(w http.ResponseWriter, r *http.Request) {
	runs, err := h.store.ListRuns("timestamp", "desc")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(runs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.SetPathValue("id", runs[0].ID)
	h.HandleRunEvents(w, r)
}

// HandleStorageStatus returns the current storage configuration status.
func (h *Handlers) HandleStorageStatus(w http.ResponseWriter, _ *http.Request) {
	resp := &StorageStatusResponse{
		Configured: false,
	}
	if h.storageConfig != nil {
		resp.Configured = h.storageConfig.Configured
		resp.Provider = h.storageConfig.Provider
		resp.Account = h.storageConfig.Account
	}
	writeJSON(w, http.StatusOK, resp)
}

// RegisterRoutes registers all web API routes on the given mux.
func RegisterRoutes(mux *http.ServeMux, store RunStore) {
	h := NewHandlers(store)
	mux.HandleFunc("GET /api/health", h.HandleHealth)
	mux.HandleFunc("GET /api/summary", h.HandleSummary)
	mux.HandleFunc("GET /api/events", h.HandleLatestRunEvents)
	mux.HandleFunc("GET /api/runs", h.HandleRuns)
	mux.HandleFunc("GET /api/runs/{id}", h.HandleRunDetail)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", h.HandleRunEvents)
	mux.HandleFunc("GET /api/storage/status", h.HandleStorageStatus)
}

// RegisterRoutesWithStorage registers all web API routes with storage config.
func RegisterRoutesWithStorage(mux *http.ServeMux, store RunStore, cfg *StorageConfig) {
	h := NewHandlersWithStorage(store, cfg)
	mux.HandleFunc("GET /api/health", h.HandleHealth)
	mux.HandleFunc("GET /api/summary", h.HandleSummary)
	mux.HandleFunc("GET /api/events", h.HandleLatestRunEvents)
	mux.HandleFunc("GET /api/runs", h.HandleRuns)
	mux.HandleFunc("GET /api/runs/{id}", h.HandleRunDetail)
	mux.HandleFunc("GET /api/v1/runs/{id}/events", h.HandleRunEvents)
	mux.HandleFunc("GET /api/storage/status", h.HandleStorageStatus)
}

// CORSMiddleware wraps a handler with CORS headers.
// If allowedOrigins is empty, no CORS header is set (same-origin only).
// Otherwise, the request Origin is checked against the allowed list.
func CORSMiddleware(next http.Handler, allowedOrigins ...string) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if len(allowedOrigins) > 0 && origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, ErrorResponse{Error: msg, Code: code})
}

func runIDFromEventsPath(path string) string {
	const prefix = "/api/v1/runs/"
	const suffix = "/events"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	id = strings.Trim(id, "/")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

type reloadableRunStore interface {
	Reload() error
}

func (h *Handlers) getRunForEvents(id string) (*RunDetail, error) {
	if store, ok := h.store.(reloadableRunStore); ok {
		if err := store.Reload(); err != nil {
			return nil, err
		}
	}
	return h.store.GetRun(id)
}
