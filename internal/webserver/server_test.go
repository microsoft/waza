package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/microsoft/waza/web"
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

// TestEmbeddedAssetsDirectoryContainsBundles verifies that the embedded
// web/dist filesystem includes at least one JS and one CSS bundle inside
// the assets/ subdirectory.  A missing assets/ directory (or empty one) is
// exactly the bug that caused the blank-page issue.
func TestEmbeddedAssetsDirectoryContainsBundles(t *testing.T) {
	distFS, err := fs.Sub(web.Assets, "dist")
	require.NoError(t, err, "fs.Sub for dist should succeed")

	entries, err := fs.ReadDir(distFS, "assets")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("skipping: web/dist/assets not built (run 'cd web && npm run build' or 'make build-web')")
	}
	require.NoError(t, err, "unexpected error reading dist/assets")

	var hasJS, hasCSS bool
	for _, e := range entries {
		if !e.IsDir() {
			switch {
			case filepath.Ext(e.Name()) == ".js":
				hasJS = true
			case filepath.Ext(e.Name()) == ".css":
				hasCSS = true
			}
		}
	}
	assert.True(t, hasJS, "dist/assets must contain at least one .js bundle")
	assert.True(t, hasCSS, "dist/assets must contain at least one .css bundle")
}

// TestIndexHTMLReferencesExistingAssets parses the embedded index.html for
// <script src="..."> and <link href="..."> tags pointing at /assets/*, then
// verifies that every referenced file actually exists in the embedded FS and
// is served with the correct content type — not the SPA HTML fallback.
func TestIndexHTMLReferencesExistingAssets(t *testing.T) {
	distFS, err := fs.Sub(web.Assets, "dist")
	require.NoError(t, err)

	if _, err := fs.ReadDir(distFS, "assets"); errors.Is(err, fs.ErrNotExist) {
		t.Skip("skipping: web/dist/assets not built (run 'cd web && npm run build' or 'make build-web')")
	} else if err != nil {
		require.NoError(t, err, "unexpected error reading dist/assets")
	}

	indexBytes, err := fs.ReadFile(distFS, "index.html")
	require.NoError(t, err, "index.html must be readable")
	html := string(indexBytes)

	// Extract asset paths referenced in index.html.
	scriptRe := regexp.MustCompile(`<script[^>]+src="(/assets/[^"]+)"`)
	linkRe := regexp.MustCompile(`<link[^>]+href="(/assets/[^"]+)"`)

	var refs []string
	for _, m := range scriptRe.FindAllStringSubmatch(html, -1) {
		refs = append(refs, m[1])
	}
	for _, m := range linkRe.FindAllStringSubmatch(html, -1) {
		refs = append(refs, m[1])
	}
	require.NotEmpty(t, refs, "index.html must reference at least one asset in /assets/")

	handler := newTestServer(t)

	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, ref, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)

			ct := rec.Header().Get("Content-Type")

			// The critical assertion: the Content-Type must NOT be
			// text/html. Before the fix, missing assets would
			// silently return index.html via the SPA fallback,
			// causing a blank page.
			assert.NotContains(t, ct, "text/html",
				"asset %s was served as text/html (SPA fallback); bundle is missing", ref)

			switch filepath.Ext(ref) {
			case ".js":
				assert.Contains(t, ct, "javascript",
					"JS bundle should be served with a javascript content type")
			case ".css":
				assert.Contains(t, ct, "css",
					"CSS bundle should be served with a css content type")
			}
		})
	}
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

	addr, _ := ln.Addr().(*net.TCPAddr)
	return addr.Port
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
