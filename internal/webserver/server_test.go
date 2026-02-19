package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	srv, err := New(Config{
		Port:       0,
		ResultsDir: t.TempDir(),
		NoBrowser:  true,
	})
	require.NoError(t, err)
	return srv.Handler()
}

func TestHealthEndpoint(t *testing.T) {
	handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]string
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Equal(t, "ok", body["status"])
}

func TestAPISummaryReturnsJSON(t *testing.T) {
	handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	require.NoError(t, err)
	assert.Contains(t, body, "totalRuns")
}

func TestSPAServesIndexHTML(t *testing.T) {
	handler := newTestServer(t)

	// Root path should return index.html
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<!doctype html>")
	assert.Contains(t, rec.Body.String(), "waza")
}

func TestSPAFallbackForClientRoutes(t *testing.T) {
	handler := newTestServer(t)

	// A client-side route like /dashboard should return index.html
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<!doctype html>")
}

func TestStaticAssetServing(t *testing.T) {
	handler := newTestServer(t)

	// favicon.svg should be served directly
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<svg")
}
