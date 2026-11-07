package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, string(body), string(append(bytes.TrimRight(body, " \t\r\n"), '\n')))
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
	over := bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes+1)
	postCheckpointOperation(t, server, "/api/v1/checkpoints", over, http.StatusRequestEntityTooLarge)
	require.Zero(t, revisions.Load())
	result := postCheckpointOperation(t, server, "/api/v1/checkpoints", checkpointOperationWire(t), http.StatusServiceUnavailable)
	require.Equal(t, "checkpoint_build_incompatible", result.Diagnostics[0].Code)
	require.Equal(t, int32(1), revisions.Load())
	require.Zero(t, opens.Load())
	require.Zero(t, factories.Load())
}

func TestCheckpointOperationHTTPDeniedOriginalPreservesStoredTerminal(t *testing.T) {
	directory := t.TempDir()
	var factories atomic.Int32
	core := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	wire := checkpointOperationWire(t)
	private := "original-private-terminal-control"
	wire = bytes.Replace(wire, []byte("- hello"), []byte("- "+private), 1)
	require.Contains(t, string(wire), private)
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	legacy, err := runnercheckpoint.NewService(core, strings.Repeat("c", 40))
	require.NoError(t, err)
	original, err := legacy.Submit(t.Context(), wire, backend, runnercheckpoint.Hooks{})
	require.NoError(t, err)
	require.NotEmpty(t, original.Submission.Terminal.Diagnostics)
	owner, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{
		ExecutionID:    original.Submission.Reservation.ExecutionID,
		ManifestDigest: original.Submission.Completion.ManifestDigest,
	}
	stored, err := owner.ReadCompleted(t.Context(), source)
	require.NoError(t, err)
	terminal := bytes.Clone(stored.Publication.ArtifactBytes["terminal.json"])
	require.NoError(t, closeBackend())
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: core, revision: func() (string, error) { return strings.Repeat("c", 40), nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	denied := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+original.FinalizedDigest+"/resume",
		wire, http.StatusInternalServerError)
	require.True(t, denied.SourceWithheld)
	require.Nil(t, denied.Source)
	require.Nil(t, denied.Submission.Terminal)
	require.Nil(t, denied.Submission.Reservation)
	require.Nil(t, denied.Submission.Completion)
	require.Nil(t, denied.RestoredUnits)
	require.Equal(t, int32(1), factories.Load())
	public, err := json.Marshal(denied)
	require.NoError(t, err)
	require.NotContains(t, string(public), private)
	backend, closeBackend, err = artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	owner, err = executionledger.New(backend)
	require.NoError(t, err)
	after, err := owner.ReadCompleted(t.Context(), source)
	require.NoError(t, err)
	require.Equal(t, terminal, after.Publication.ArtifactBytes["terminal.json"])
	require.NoError(t, closeBackend())
}

type checkpointRestoreFault struct {
	artifactmanifest.Backend
	key  string
	err  error
	data []byte
}

func (b checkpointRestoreFault) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if key == b.key {
		return bytes.Clone(b.data), b.err
	}
	return b.Backend.Get(ctx, key, limit)
}

func TestCheckpointOperationHTTPFailedRestoreNeverConstructsFactory(t *testing.T) {
	for _, fault := range []string{"checkpoint-missing", "checkpoint-corrupt", "final-marker-missing", "storage-unknown"} {
		t.Run(fault, func(t *testing.T) {
			directory := t.TempDir()
			var factories atomic.Int32
			core := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories.Add(1)
				return execution.NewMockEngine(config.ModelID), nil
			})
			var readFault atomic.Pointer[checkpointRestoreFault]
			mux := http.NewServeMux()
			registerCheckpointOperationRoutes(mux, checkpointOperations{
				open: func() (artifactmanifest.Backend, func() error, error) {
					backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
					if fault := readFault.Load(); err == nil && fault != nil {
						copy := *fault
						copy.Backend = backend
						backend = copy
					}
					return backend, closeBackend, err
				},
				runner: core, revision: func() (string, error) { return strings.Repeat("c", 40), nil },
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			wire := checkpointOperationWire(t)
			created := postCheckpointOperation(t, server, "/api/v1/checkpoints", wire, http.StatusOK)
			next := &checkpointRestoreFault{key: "blob-" + created.FinalizedDigest, err: artifactmanifest.ErrMissing}
			switch fault {
			case "checkpoint-corrupt":
				next.err, next.data = nil, []byte(`{"private":"corrupt-checkpoint-control"}`)
			case "final-marker-missing":
				next.key = "final-" + created.Submission.Completion.ManifestDigest
			case "storage-unknown":
				next.err = errors.New("private-storage-failure-control")
			}
			readFault.Store(next)
			rejected := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.FinalizedDigest+"/resume",
				wire, http.StatusConflict)
			require.Equal(t, "rejected", rejected.Status)
			require.Nil(t, rejected.Source)
			require.Nil(t, rejected.Submission)
			require.Nil(t, rejected.RestoredUnits)
			require.Equal(t, int32(1), factories.Load())
			public, err := json.Marshal(rejected)
			require.NoError(t, err)
			require.NotContains(t, string(public), "private")
		})
	}
}

func TestCheckpointOperationHTTPBeforeRestoreCreatesApprovedFreshParent(t *testing.T) {
	directory := t.TempDir()
	var factories atomic.Int32
	core := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	service, err := runnercheckpoint.NewService(core, strings.Repeat("c", 40))
	require.NoError(t, err)
	wire := checkpointOperationWire(t)
	stopped, err := service.SubmitCaptured(t.Context(), wire, backend, runnercheckpoint.Hooks{
		Published: func(context.Context, runnercheckpoint.Reference) error {
			return errors.New("stop after actual before-unit publication")
		},
	})
	require.Error(t, err)
	require.NotEmpty(t, stopped.BeforeDigest)
	require.Empty(t, stopped.FinalizedDigest)
	require.Zero(t, factories.Load())
	require.NoError(t, closeBackend())
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: core, revision: func() (string, error) { return strings.Repeat("c", 40), nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, wire, destination)
	resumed := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+stopped.BeforeDigest+"/resume",
		destination, http.StatusOK)
	require.False(t, resumed.SourceWithheld)
	require.NotNil(t, resumed.Source)
	require.Equal(t, 0, *resumed.RestoredUnits)
	require.Equal(t, 0, *resumed.RemainingWork)
	require.Equal(t, 1, resumed.Submission.EngineCalls)
	require.Equal(t, int32(1), factories.Load())
	require.NotEqual(t, stopped.Submission.Reservation.ExecutionID, resumed.Submission.Reservation.ExecutionID)
	require.Equal(t, stopped.Submission.Reservation.ExecutionID, resumed.Submission.Reservation.Parent.ExecutionID)
	require.Equal(t, "retry", resumed.Submission.Reservation.Parent.Relationship)
	require.Equal(t, 3, resumed.Submission.Terminal.Selection.Attempt)
}
