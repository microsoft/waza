package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestDashboardRegistersCheckpointInspection(t *testing.T) {
	server, err := New(Config{ResultsDir: t.TempDir(), NoBrowser: true, Lab: true})
	require.NoError(t, err)
	digest := strings.Repeat("a", 64)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/"+digest, nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "uncreated immutable store must not become an SPA success")
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	var result webapi.CheckpointInspection
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, digest, result.CheckpointSHA256)
	require.Equal(t, "unavailable", result.Availability)
	require.Nil(t, result.Checkpoint)
	require.Nil(t, result.RestoredUnits)
	require.Nil(t, result.RemainingWork)
}

func TestDashboardRegistersProtectedCheckpointOperations(t *testing.T) {
	server, err := New(Config{ResultsDir: t.TempDir(), NoBrowser: true, Lab: true})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	for _, path := range []string{"/api/v1/checkpoints", "/api/v1/checkpoints/" + strings.Repeat("a", 64) + "/resume"} {
		response, err := httpServer.Client().Post(httpServer.URL+path, "application/json", bytes.NewBufferString("{}"))
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		result, err := runnercheckpoint.ParseResult(body)
		require.NoError(t, err)
		require.Equal(t, "checkpoint_input_invalid", result.Diagnostics[0].Code)
		require.Nil(t, result.Submission)
		require.Nil(t, result.Source)
		require.Nil(t, result.RestoredUnits)
		require.Equal(t, "6", response.Header.Get("X-Waza-Exit-Code"))
	}
}
