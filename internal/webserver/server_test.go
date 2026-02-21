package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

func TestHealthEndpointWrongMethodFallsBackToSPA(t *testing.T) {
	handler := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<!doctype html>")
}

func TestWebSocketUpgradeRequestFallsBackToSPA(t *testing.T) {
	ts := httptest.NewServer(newTestServer(t))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/ws", nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), "<!doctype html>")
}

func TestNewAppliesDefaults(t *testing.T) {
	srv, err := New(Config{
		NoBrowser: true,
		Logger:    discardLogger(),
	})
	require.NoError(t, err)

	assert.Equal(t, 3000, srv.cfg.Port)
	assert.Equal(t, ".", srv.cfg.ResultsDir)
	assert.Equal(t, "127.0.0.1:3000", srv.srv.Addr)
	assert.NotNil(t, srv.Handler())
}

func TestListenAndServeShutsDownOnContextCancel(t *testing.T) {
	port := freePort(t)
	srv, err := New(Config{
		Port:       port,
		ResultsDir: t.TempDir(),
		NoBrowser:  true,
		Logger:     discardLogger(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx)
	}()

	waitForHealthEndpoint(t, port)
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down after context cancellation")
	}
}

func TestListenAndServeWrapsStartupError(t *testing.T) {
	logger := discardLogger()
	srv := &Server{
		cfg: Config{
			Port:      1,
			NoBrowser: true,
			Logger:    logger,
		},
		srv: &http.Server{
			Addr:    "127.0.0.1:-1",
			Handler: http.NewServeMux(),
		},
		logger: logger,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.ListenAndServe(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP server error")
}

func TestOpenBrowser(t *testing.T) {
	command := browserCommandName()
	if command == "" {
		t.Skip("unsupported test platform for openBrowser")
	}

	t.Run("success", func(t *testing.T) {
		tmpDir := t.TempDir()
		scriptPath := filepath.Join(tmpDir, command)
		err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)
		require.NoError(t, err)
		t.Setenv("PATH", tmpDir)

		require.NoError(t, openBrowser("http://localhost:9999"))
	})

	t.Run("command not found", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		require.Error(t, openBrowser("http://localhost:9999"))
	})
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, ln.Close())
	}()

	return ln.Addr().(*net.TCPAddr).Port
}

func waitForHealthEndpoint(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{
		Timeout: 200 * time.Millisecond,
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", port)
	deadline := time.Now().Add(3 * time.Second)

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)

		resp, err := client.Do(req)
		if err == nil {
			_, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr)
			require.NoError(t, resp.Body.Close())
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatalf("server did not become ready at %s", url)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func browserCommandName() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	case "linux":
		return "xdg-open"
	default:
		return ""
	}
}
