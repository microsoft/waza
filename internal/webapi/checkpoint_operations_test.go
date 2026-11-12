package webapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnercheckpoint/testfixture"
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

func TestCheckpointOperationHTTPCloseFailurePreservesAdmittedResult(t *testing.T) {
	directory := t.TempDir()
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	revision := strings.Repeat("c", 40)
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
			if err != nil {
				return nil, nil, err
			}
			return backend, func() error {
				return errors.Join(closeBackend(), errors.New("private-http-close-control"))
			}, nil
		},
		runner: service, revision: func() (string, error) { return revision, nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	wire := checkpointOperationWire(t)
	created := postCheckpointOperation(t, server, "/api/v1/checkpoints", wire, http.StatusServiceUnavailable)
	require.Equal(t, "1.1", created.SchemaVersion)
	require.Equal(t, "operation_error", created.Status)
	require.Equal(t, runnercheckpoint.ErrorCodeStorageCloseFailed, created.ErrorCode)
	require.Equal(t, 2, created.ExitCode)
	require.NotNil(t, created.Submission.Completion)
	require.NotEmpty(t, created.BeforeDigest)
	require.NotEmpty(t, created.FinalizedDigest)
	createdData, err := runnercheckpoint.EncodeResult(created)
	require.NoError(t, err)
	require.NotContains(t, string(createdData), "private-http-close-control")

	destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, wire, destination)
	reused := postCheckpointOperation(t, server,
		"/api/v1/checkpoints/"+created.FinalizedDigest+"/resume", destination, http.StatusServiceUnavailable)
	require.Equal(t, "operation_error", reused.Status)
	require.Equal(t, 2, reused.ExitCode)
	require.Equal(t, runnercheckpoint.ErrorCodeStorageCloseFailed, reused.ErrorCode)
	require.Equal(t, "reused", reused.Submission.Status)
	require.Equal(t, 0, reused.Submission.EngineCalls)
	require.NotNil(t, reused.Source)
	require.Equal(t, created.Submission.Reservation.ExecutionID, reused.Source.ExecutionID)
	reusedData, err := runnercheckpoint.EncodeResult(reused)
	require.NoError(t, err)
	require.NotContains(t, string(reusedData), "private-http-close-control")
}

func TestCheckpointOperationHTTPCombinedPinnedReadAndCloseFailures(t *testing.T) {
	directory := t.TempDir()
	var factories atomic.Int32
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	revision := strings.Repeat("c", 40)
	local, closeLocal, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	checkpoints, err := runnercheckpoint.NewService(service, revision)
	require.NoError(t, err)
	wire := checkpointOperationWire(t)
	created, err := checkpoints.SubmitCaptured(t.Context(), wire, local, runnercheckpoint.Hooks{})
	require.NoError(t, err)
	require.NoError(t, closeLocal())
	backend, fixture := testfixture.NewPinnedReadBackend(t, directory)
	require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	logs := &checkpointLogSink{}
	setWebAPITestLogger(t, slog.New(slog.NewTextHandler(logs, nil)))
	defer func() {
		require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	}()
	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return backend, func() error { return errors.New("private-http-close-control") }, nil
		},
		runner: service, revision: func() (string, error) { return revision, nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	result := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.FinalizedDigest+"/resume",
		wire, http.StatusServiceUnavailable)
	require.Equal(t, int32(4), fixture.Reads())
	require.Equal(t, int32(1), factories.Load())
	require.Equal(t, "operation_error", result.Status)
	require.Equal(t, 2, result.ExitCode)
	require.Equal(t, runnercheckpoint.ErrorCodeStorageCloseFailed, result.ErrorCode)
	require.True(t, result.SourceWithheld)
	require.Nil(t, result.Source)
	require.NotNil(t, result.Submission)
	require.Equal(t, "submission_error", result.Submission.Status)
	require.Equal(t, "reuse_not_admitted", result.Submission.Diagnostics[0].Code)
	require.Nil(t, result.Submission.Reservation)
	require.Nil(t, result.Submission.Completion)
	require.Eventually(t, func() bool {
		record := logs.String()
		return strings.Contains(record, testfixture.PinnedReadFailure) &&
			strings.Contains(record, "private-http-close-control")
	}, 2*time.Second, time.Millisecond)
	public, err := runnercheckpoint.EncodeResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(public), testfixture.PinnedReadFailure)
	require.NotContains(t, string(public), "private-http-close-control")
}

func TestCheckpointOperationHTTPDeniedReuseWithholdsSourceAndPreservesOriginal(t *testing.T) {
	directory := t.TempDir()
	var factories atomic.Int32
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine(config.ModelID), nil
	})
	revision := strings.Repeat("c", 40)
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	legacy, err := runnercheckpoint.NewService(service, revision)
	require.NoError(t, err)
	const private = "original-http-private-terminal-control"
	input := checkpointOperationWire(t)
	wire := bytes.Replace(input, []byte("- hello"), []byte("- "+private), 1)
	require.NotEqual(t, input, wire)
	for _, replacement := range [][2]string{
		{"id: selected", "id: " + private},
		{`"task":"selected"`, `"task":"` + private + `"`},
	} {
		changed := bytes.Replace(wire, []byte(replacement[0]), []byte(replacement[1]), 1)
		require.NotEqual(t, wire, changed)
		wire = changed
	}
	original, err := legacy.Submit(t.Context(), wire, backend, runnercheckpoint.Hooks{})
	require.NoError(t, err)
	require.NotEmpty(t, original.FinalizedDigest)
	require.NotEmpty(t, original.Submission.Terminal.Diagnostics)
	require.NotEqual(t, "[REDACTED]", original.Submission.Terminal.Diagnostics[0].Message)
	owner, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{
		ExecutionID: original.Submission.Reservation.ExecutionID, ManifestDigest: original.Submission.Completion.ManifestDigest,
	}
	before, err := owner.ReadCompleted(t.Context(), source)
	require.NoError(t, err)
	var names []string
	for _, artifact := range before.Publication.Manifest.Artifacts {
		names = append(names, artifact.Name)
	}
	require.NotEmpty(t, names)
	before, err = owner.ReadCompleted(t.Context(), source, names...)
	require.NoError(t, err)
	require.Len(t, before.Publication.ArtifactBytes, len(names))
	require.Contains(t, string(before.Publication.ArtifactBytes["terminal.json"]), private)
	checkpointBefore, err := backend.Get(t.Context(), "blob-"+original.FinalizedDigest, executioncheckpoint.MaxBytes)
	require.NoError(t, err)
	require.NoError(t, closeBackend())

	mux := http.NewServeMux()
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: service, revision: func() (string, error) { return revision, nil },
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	denied := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+original.FinalizedDigest+"/resume", wire, http.StatusInternalServerError)
	require.Equal(t, "submission_error", denied.Status)
	require.Equal(t, 2, denied.ExitCode)
	require.True(t, denied.SourceWithheld)
	require.Nil(t, denied.Source)
	require.Nil(t, denied.RestoredUnits)
	require.Nil(t, denied.RemainingWork)
	require.NotNil(t, denied.Submission)
	require.Zero(t, denied.Submission.EngineCalls)
	require.Empty(t, denied.Submission.Report)
	require.Nil(t, denied.Submission.Terminal)
	require.Nil(t, denied.Submission.Completion)
	require.Nil(t, denied.Submission.Reservation)
	public, err := json.Marshal(denied)
	require.NoError(t, err)
	require.NotContains(t, string(public), private)
	require.Equal(t, int32(1), factories.Load())

	backend, closeBackend, err = artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeBackend()) }()
	owner, err = executionledger.New(backend)
	require.NoError(t, err)
	after, err := owner.ReadCompleted(t.Context(), source, names...)
	require.NoError(t, err)
	require.Equal(t, before.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
	require.Equal(t, before.Publication.ManifestBytes, after.Publication.ManifestBytes)
	require.Equal(t, before.Reservation.Bytes, after.Reservation.Bytes)
	require.Equal(t, before.CompletionBytes, after.CompletionBytes)
	checkpointAfter, err := backend.Get(t.Context(), "blob-"+original.FinalizedDigest, executioncheckpoint.MaxBytes)
	require.NoError(t, err)
	require.Equal(t, checkpointBefore, checkpointAfter)
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
	invalidInput := postCheckpointOperation(t, server, "/api/v1/checkpoints", []byte("{}"), http.StatusBadRequest)
	require.Equal(t, "1.0", invalidInput.SchemaVersion)
	require.Equal(t, "rejected", invalidInput.Status)
	require.Equal(t, 6, invalidInput.ExitCode)
	require.Equal(t, "checkpoint_input_invalid", invalidInput.Diagnostics[0].Code)
	invalidDigest := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+strings.Repeat("A", 64)+"/resume",
		checkpointOperationWire(t), http.StatusBadRequest)
	require.Equal(t, "1.0", invalidDigest.SchemaVersion)
	require.Equal(t, "rejected", invalidDigest.Status)
	require.Equal(t, 6, invalidDigest.ExitCode)
	oversized := postCheckpointOperation(t, server, "/api/v1/checkpoints",
		bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes+1), http.StatusRequestEntityTooLarge)
	require.Equal(t, "1.0", oversized.SchemaVersion)
	require.Equal(t, "rejected", oversized.Status)
	require.Equal(t, 6, oversized.ExitCode)
	require.Zero(t, revisions.Load())
	result := postCheckpointOperation(t, server, "/api/v1/checkpoints", checkpointOperationWire(t), http.StatusServiceUnavailable)
	require.Equal(t, "operation_error", result.Status)
	require.Equal(t, runnercheckpoint.ErrorCodeBuildUnavailable, result.ErrorCode)
	require.Equal(t, int32(1), revisions.Load())
	require.Zero(t, opens.Load())
	require.Zero(t, factories.Load())
}

type checkpointLogSink struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (s *checkpointLogSink) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Write(data)
}

func (s *checkpointLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.String()
}

func (s *checkpointLogSink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Reset()
}

func setWebAPITestLogger(t *testing.T, logger *slog.Logger) {
	t.Helper()
	previous := slog.Default()
	writer, flags := log.Writer(), log.Flags()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(previous)
		// Restoring slog's default handler does not restore log's writer or flags.
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
}

func TestCheckpointLoggingRestoresStandardLogger(t *testing.T) {
	previous := slog.Default()
	writer, flags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	var original bytes.Buffer
	log.SetOutput(&original)
	log.SetFlags(log.Llongfile)
	t.Run("capture", func(t *testing.T) {
		records := &checkpointLogSink{}
		setWebAPITestLogger(t, slog.New(slog.NewTextHandler(records, nil)))
		log.Print("captured standard log")
		slog.Error("captured structured log")
		require.Contains(t, records.String(), "captured standard log")
		require.Contains(t, records.String(), "captured structured log")
	})
	require.Same(t, previous, slog.Default())
	require.Same(t, &original, log.Writer())
	require.Equal(t, log.Llongfile, log.Flags())
	log.Print("restored standard log")
	require.Contains(t, original.String(), "restored standard log")
}

func TestCheckpointOperationHTTPSubmissionParseFailureLogsWithoutRequestContents(t *testing.T) {
	require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	records := &checkpointLogSink{}
	setWebAPITestLogger(t, slog.New(slog.NewTextHandler(records, nil)))
	defer func() {
		require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	}()

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
			return "", errors.New("must not read revision")
		},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	const secret = "private-submission-marker"
	for _, action := range []string{"create", "resume"} {
		for _, input := range []struct {
			name string
			wire string
		}{
			{"malformed", `{"private":"` + secret + `",`},
			{"schema-invalid", `{"kind":"` + secret + `","schema_version":"1.0","request":{}}`},
		} {
			t.Run(action+"-"+input.name, func(t *testing.T) {
				require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
				records.Reset()
				_, parseErr := runnerunit.ParseSubmission([]byte(input.wire))
				require.Error(t, parseErr)
				path := "/api/v1/checkpoints"
				if action == "resume" {
					path += "/" + strings.Repeat("e", 64) + "/resume"
				}
				response, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(input.wire))
				require.NoError(t, err)
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.NoError(t, response.Body.Close())
				require.Equal(t, http.StatusBadRequest, response.StatusCode)
				require.Equal(t, "6", response.Header.Get("X-Waza-Exit-Code"))
				result, err := runnercheckpoint.ParseResult(body)
				require.NoError(t, err)
				require.Equal(t, "rejected", result.Status)
				require.Equal(t, 6, result.ExitCode)
				require.Equal(t, "checkpoint_input_invalid", result.Diagnostics[0].Code)
				require.NotContains(t, string(body), secret)
				require.Eventually(t, func() bool {
					record := records.String()
					return strings.Contains(record, "checkpoint submission parsing failed") &&
						strings.Contains(record, "action="+action)
				}, 2*time.Second, time.Millisecond, "checkpoint submission parsing diagnostic was not logged")
				record := records.String()
				require.Contains(t, record, "Inspect the submission and local schema initialization.")
				require.NotContains(t, record, secret)
				require.NotContains(t, record, parseErr.Error())
			})
		}
	}
	require.Zero(t, opens.Load())
	require.Zero(t, revisions.Load())
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
