package webapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func postCheckpointAvailability(t *testing.T, server *httptest.Server, digest string, wire []byte, header func(*http.Request), want int) runnercheckpoint.Availability {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/api/v1/checkpoints/"+digest+"/availability", bytes.NewReader(wire))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if header != nil {
		header(request)
	}
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, want, response.StatusCode, "%s", body)
	result, err := runnercheckpoint.ParseAvailability(body)
	require.NoError(t, err, "%s", body)
	require.Equal(t, response.Header.Get("X-Waza-Exit-Code"), string(rune('0'+result.ExitCode)))
	return *result
}

func TestCheckpointAvailabilityHTTPReadOnlyPreflight(t *testing.T) {
	directory := t.TempDir()
	var factories, opens atomic.Int32
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			opens.Add(1)
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: service, revision: func() (string, error) { return strings.Repeat("c", 40), nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wire := checkpointOperationWire(t)
	created := postCheckpointOperation(t, server, "/api/v1/checkpoints", wire, http.StatusOK)
	require.Equal(t, int32(1), factories.Load())

	available := postCheckpointAvailability(t, server, created.FinalizedDigest, wire, nil, http.StatusOK)
	require.Equal(t, runnercheckpoint.ResumeReuseOriginalTerminal, available.ResumeAction)
	require.Equal(t, "finalized-unit", available.Source.Boundary)
	stale := postCheckpointAvailability(t, server, created.BeforeDigest, wire, nil, http.StatusConflict)
	require.Equal(t, "checkpoint_stale", stale.Diagnostics[0].Code)
	require.Nil(t, stale.Source)
	invalid := postCheckpointAvailability(t, server, "ABC", wire, nil, http.StatusBadRequest)
	require.Equal(t, "checkpoint_digest_invalid", invalid.Diagnostics[0].Code)
	require.Equal(t, int32(1), factories.Load())

	before := opens.Load()
	denied := postCheckpointAvailability(t, server, created.FinalizedDigest, wire,
		func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, http.StatusForbidden)
	require.Equal(t, "operation_http_denied", denied.Diagnostics[0].Code)
	require.Equal(t, before, opens.Load())
}

func TestCheckpointAvailabilityHTTPStorageFailureIsOperationError(t *testing.T) {
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return nil, nil, errors.New("private provider path")
		},
		runner: runnerunit.NewService(nil), revision: func() (string, error) { return strings.Repeat("c", 40), nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	result := postCheckpointAvailability(t, server, strings.Repeat("a", 64), checkpointOperationWire(t), nil, http.StatusServiceUnavailable)
	require.Equal(t, "operation_error", result.Status)
	require.Equal(t, runnercheckpoint.ErrorCodeStorageUnavailable, result.Diagnostics[0].Code)
	require.NotContains(t, result.Diagnostics[0].Message, "private provider path")
}
