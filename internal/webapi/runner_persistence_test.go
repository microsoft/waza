//go:build linux || darwin

package webapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerHTTPPersistsEveryAdmittedTerminal(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "canceled"}[canceled], func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			data, err := os.ReadFile("../runnercontract/testdata/request.json")
			require.NoError(t, err)
			mux := http.NewServeMux()
			RegisterRunnerRoutes(mux, dir)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
			request.Host = "localhost"
			request.Header.Set("Content-Type", "application/json")
			if canceled {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			result, err := runnercontract.ParseResult(response.Body.Bytes())
			require.NoError(t, err)
			expected := runnercontract.Success
			if canceled {
				expected = runnercontract.Canceled
			}
			require.Equal(t, expected, result.Outcome)
			saved, err := readRunnerResults(dir)
			require.NoError(t, err)
			require.Len(t, saved, 1)
			require.Equal(t, *result, saved[0])
			// Replays remain separate observed invocations, never overwrites.
			_, _, err = persistRunnerResult(dir, result)
			require.NoError(t, err)
			saved, err = readRunnerResults(dir)
			require.NoError(t, err)
			require.Len(t, saved, 2)
		})
	}
}

func TestRunnerPublicationFailureRetainsEvidence(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "not-a-directory")
	require.NoError(t, os.WriteFile(dir, []byte("untouched"), 0o600))
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	result, err := runnercontract.ParseResult(response.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, runnercontract.InfrastructureFailure, result.Outcome)
	require.Equal(t, "terminalization", result.Boundary)
	require.Equal(t, "publication_failed", result.Diagnostics[len(result.Diagnostics)-1].Code)
	require.NotNil(t, result.Observation)
	content, err := os.ReadFile(dir)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(content))
}

func TestRunnerPublicationRejectsSymlinkDirectory(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(base, "results")
	require.NoError(t, os.Symlink(outside, link))
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	_, _, err = persistRunnerResult(link, &result)
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRunnerLabWithholdsQuarantinedArtifact(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	_, _, err = persistRunnerResult(dir, &result)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Join(dir, ".runner"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	name := entries[0].Name()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".runner", "."+name+".publication-failed"), []byte("withheld\n"), 0o600))

	saved, err := readRunnerResults(dir)
	require.NoError(t, err)
	require.Empty(t, saved)
	page, err := readRunnerPage(t.Context(), dir, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Empty(t, page.Results[0].Result)
	require.Contains(t, page.Results[0].Error, "withheld")
}

func TestRunnerLabRejectsFIFOWithoutOpeningOrBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replacement.runner.json")
	require.NoError(t, unix.Mkfifo(path, 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := readRunnerResults(dir)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("FIFO blocked artifact inspection")
	}
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer root.Close() //nolint:errcheck
	// Exercise the replacement-race path independently of ReadDir's type.
	file, err := openRunnerArtifact(root, filepath.Base(path))
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	require.False(t, info.Mode().IsRegular())
	require.NoError(t, file.Close())
}
