package webapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestCheckpointConfiguredBackendSharesManifestStore(t *testing.T) {
	for _, control := range []struct {
		name   string
		config *projectconfig.StorageConfig
		deny   bool
	}{
		{"default-local", nil, false},
		{"enabled-local", &projectconfig.StorageConfig{Enabled: true, Provider: "local"}, false},
		{"disabled-foreign", &projectconfig.StorageConfig{Provider: "unsupported"}, false},
		{"enabled-foreign", &projectconfig.StorageConfig{Enabled: true, Provider: "unsupported"}, true},
	} {
		t.Run(control.name, func(t *testing.T) {
			directory := t.TempDir()
			original := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(directory, ".waza-artifacts"), 0o700))
			t.Chdir(original)
			backend, closeBackend, err := checkpointBackendFor(directory, control.config)()
			if control.deny {
				require.Error(t, err)
				require.Nil(t, backend)
				require.Nil(t, closeBackend)
				return
			}
			require.NoError(t, err)
			data := []byte("configured-checkpoint-store-control")
			digest := artifactmanifest.Hash(data)
			created, err := backend.Create(t.Context(), "blob-"+digest, data)
			require.NoError(t, err)
			require.True(t, created)
			require.NoError(t, closeBackend())
			physical, err := os.ReadFile(filepath.Join(directory, ".waza-artifacts", "blob-"+digest))
			require.NoError(t, err)
			require.Equal(t, data, physical)
			require.NoDirExists(t, filepath.Join(original, ".waza-artifacts"))
		})
	}
}

func checkpointOperationWire(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	return wire
}

func postCheckpointOperation(t *testing.T, server *httptest.Server, path string, wire []byte, status int) runnercheckpoint.Result {
	t.Helper()
	response, err := server.Client().Post(server.URL+path, "application/json", bytes.NewReader(wire))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, status, response.StatusCode, "%s", body)
	require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
	result, err := runnercheckpoint.ParseResult(body)
	require.NoError(t, err, "%s", body)
	require.Equal(t, strconv.Itoa(result.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
	return *result
}

func TestCheckpointOperationActualHTTPProducerAndFailedRestore(t *testing.T) {
	directory := t.TempDir()
	var factories atomic.Int32
	var revision atomic.Pointer[string]
	initialRevision, changedRevision := strings.Repeat("c", 40), strings.Repeat("d", 40)
	revision.Store(&initialRevision)
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: service, revision: func() (string, error) { return *revision.Load(), nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wire := checkpointOperationWire(t)
	metadata := "http-retained-checkpoint-metadata-control"
	changed := bytes.Replace(wire, []byte("id: selected"), []byte("id: "+metadata), 1)
	require.NotEqual(t, wire, changed)
	wire = changed
	changed = bytes.Replace(wire, []byte(`"task":"selected"`), []byte(`"task":"`+metadata+`"`), 1)
	require.NotEqual(t, wire, changed)
	wire = changed
	created := postCheckpointOperation(t, server, "/api/v1/checkpoints", wire, http.StatusOK)
	require.NotNil(t, created.Submission.Completion)
	require.Equal(t, int32(1), factories.Load())
	require.Empty(t, created.Submission.Report)
	original, err := json.Marshal(created.Submission.Terminal)
	require.NoError(t, err)
	destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, wire, destination)
	restored := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.FinalizedDigest+"/resume", destination, http.StatusOK)
	require.Equal(t, "reused", restored.Status)
	require.Zero(t, restored.Submission.EngineCalls)
	require.Equal(t, int32(1), factories.Load())
	require.Equal(t, 2, restored.Submission.Terminal.Selection.Attempt)
	terminal, err := json.Marshal(restored.Submission.Terminal)
	require.NoError(t, err)
	require.Equal(t, original, terminal)
	stale := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.BeforeDigest+"/resume", wire, http.StatusConflict)
	require.True(t, stale.SourceWithheld)
	require.Nil(t, stale.Source)
	require.Nil(t, stale.RestoredUnits)
	revision.Store(&changedRevision)
	incompatible := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.FinalizedDigest+"/resume", wire, http.StatusConflict)
	require.Equal(t, "incompatible", incompatible.CompatibilityResult)
	require.Equal(t, []string{"runner_revision"}, incompatible.CompatibilityDifferences)
	require.True(t, incompatible.SourceWithheld)
	require.Nil(t, incompatible.Source)
	encoded, err := json.Marshal(incompatible)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), metadata)
	require.Equal(t, int32(1), factories.Load())
}

func TestCheckpointOperationHTTPAdmissionPrecedesBuildStorageAndExecution(t *testing.T) {
	var opens, revisions, factories atomic.Int32
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			opens.Add(1)
			return nil, nil, errors.New("private-provider-control")
		},
		runner: runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
			factories.Add(1)
			return nil, errors.New("must not construct")
		}),
		revision: func() (string, error) {
			revisions.Add(1)
			return "", errors.New("private-build-control")
		},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	postCheckpointOperation(t, server, "/api/v1/checkpoints", []byte("{}"), http.StatusBadRequest)
	postCheckpointOperation(t, server, "/api/v1/checkpoints/"+strings.Repeat("A", 64)+"/resume",
		checkpointOperationWire(t), http.StatusBadRequest)
	postCheckpointOperation(t, server, "/api/v1/checkpoints",
		bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes+1), http.StatusRequestEntityTooLarge)
	require.Zero(t, revisions.Load())
	result := postCheckpointOperation(t, server, "/api/v1/checkpoints", checkpointOperationWire(t), http.StatusServiceUnavailable)
	require.Equal(t, "checkpoint_build_incompatible", result.Diagnostics[0].Code)
	require.Equal(t, int32(1), revisions.Load())
	require.Zero(t, opens.Load())
	require.Zero(t, factories.Load())
}

func TestCheckpointOperationHTTPTrustDenialPrecedesInputAndStorage(t *testing.T) {
	var opens, revisions atomic.Int32
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			opens.Add(1)
			return nil, nil, errors.New("must not open")
		},
		runner: runnerunit.NewService(nil),
		revision: func() (string, error) {
			revisions.Add(1)
			return strings.Repeat("c", 40), nil
		},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, control := range []string{"foreign-host", "foreign-origin", "cross-site"} {
		t.Run(control, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v1/checkpoints",
				bytes.NewReader(checkpointOperationWire(t)))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			switch control {
			case "foreign-host":
				request.Host = "example.invalid"
			case "foreign-origin":
				request.Header.Set("Origin", "https://example.invalid")
			case "cross-site":
				request.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusForbidden, response.StatusCode, "%s", body)
			result, err := runnercheckpoint.ParseResult(body)
			require.NoError(t, err)
			require.Equal(t, "operation_http_denied", result.Diagnostics[0].Code)
			require.Nil(t, result.Submission)
			require.Equal(t, "6", response.Header.Get("X-Waza-Exit-Code"))
		})
	}
	require.Zero(t, opens.Load())
	require.Zero(t, revisions.Load())
}
