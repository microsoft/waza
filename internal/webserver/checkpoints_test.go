package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
