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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func postSubmission(t *testing.T, server *httptest.Server, wire []byte, status int) runnerunit.Submission {
	result, _ := postSubmissionBytes(t, server, wire, status)
	return result
}

func postSubmissionBytes(t *testing.T, server *httptest.Server, wire []byte, status int) (runnerunit.Submission, []byte) {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/api/v1/runner/submit", "application/json", bytes.NewReader(wire))
	require.NoError(t, err)
	defer response.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, status, response.StatusCode, "%s", body)
	require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
	result, err := runnerunit.ParseSubmissionResult(body)
	require.NoError(t, err, "%s", body)
	require.Equal(t, strconv.Itoa(result.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
	require.Empty(t, result.Report)
	return *result, body
}

func TestPublicSubmissionHTTPActualConcurrentDistinctOriginalAndReuse(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, ".waza-artifacts"), 0o700))
	mux := http.NewServeMux()
	RegisterSubmissionRoutes(mux, directory, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	wire := checkpointOperationWire(t)
	results := make(chan runnerunit.Submission, 2)
	var joined sync.WaitGroup
	for range 2 {
		joined.Go(func() {
			var result runnerunit.Submission
			defer func() { results <- result }()
			result = postSubmission(t, server, wire, http.StatusOK)
		})
	}
	joined.Wait()
	first, second := <-results, <-results
	require.NotNil(t, first.Reservation)
	require.NotNil(t, second.Reservation)
	require.NotEqual(t, first.Reservation.ExecutionID, second.Reservation.ExecutionID)
	require.Equal(t, first.Reservation.Logical, second.Reservation.Logical)
	require.Equal(t, first.Terminal.Selection.Attempt, second.Terminal.Selection.Attempt)
	request, err := runnerunit.ParseSubmission(wire)
	require.NoError(t, err)
	request.Request = bytes.Replace(request.Request, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.Contains(t, string(request.Request), `"attempt":3`)
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest,
	}}
	reuseWire, err := json.Marshal(request)
	require.NoError(t, err)
	reused := postSubmission(t, server, reuseWire, http.StatusOK)
	require.Equal(t, "reused", reused.Status)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, first.Terminal, reused.Terminal)
	require.Equal(t, first.Reservation, reused.Reservation)
	request.Policy.Source.ManifestDigest = strings.Repeat("a", 64)
	corruptWire, err := json.Marshal(request)
	require.NoError(t, err)
	unavailable := postSubmission(t, server, corruptWire, http.StatusConflict)
	require.Nil(t, unavailable.Terminal)
	require.Nil(t, unavailable.Reservation)
	require.Nil(t, unavailable.Completion)
	require.Zero(t, unavailable.EngineCalls)
	encoded, err := runnerunit.EncodeSubmissionResult(unavailable)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), first.Reservation.ExecutionID)
}

func TestPublicSubmissionHTTPInputAdmissionPrecedesProvider(t *testing.T) {
	var opened atomic.Int32
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{
		runner: runnerunit.NewService(nil),
		open: func() (artifactmanifest.Backend, func() error, error) {
			opened.Add(1)
			return nil, nil, errors.New("private-provider-secret-control")
		},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	postSubmission(t, server, []byte("{}"), http.StatusBadRequest)
	postSubmission(t, server, bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes+1), http.StatusRequestEntityTooLarge)
	require.Zero(t, opened.Load())
	failed := postSubmission(t, server, checkpointOperationWire(t), http.StatusServiceUnavailable)
	require.Equal(t, 2, failed.ExitCode)
	require.Equal(t, "submission_error", failed.Status)
	encoded, err := runnerunit.EncodeSubmissionResult(failed)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-provider-secret-control")
	require.Equal(t, int32(1), opened.Load())
}

func TestPublicSubmissionHTTPCloseFailurePreservesActorNotOperationSuccess(t *testing.T) {
	directory := t.TempDir()
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{
		runner: runnerunit.NewService(nil),
		open: func() (artifactmanifest.Backend, func() error, error) {
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
			return backend, func() error {
				return errors.Join(closeBackend(), errors.New("private-close-control"))
			}, err
		},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	result := postSubmission(t, server, checkpointOperationWire(t), http.StatusServiceUnavailable)
	require.Equal(t, "submission_error", result.Status)
	require.Equal(t, 2, result.ExitCode)
	require.NotNil(t, result.Terminal)
	require.Equal(t, 0, result.Terminal.ExitCode)
	require.Equal(t, 1, result.EngineCalls)
	require.Nil(t, result.Completion)
	encoded, err := runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-close-control")
}

func TestPublicSubmissionHTTPRetainedCanceledActorAndZeroNewReuse(t *testing.T) {
	directory := t.TempDir()
	var calls atomic.Int32
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
			calls.Add(1)
			return nil, context.Canceled
		}),
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wire := checkpointOperationWire(t)
	original := postSubmission(t, server, wire, http.StatusOK)
	require.Equal(t, 3, original.ExitCode)
	require.Equal(t, "completed", original.Status)
	require.Equal(t, int32(1), calls.Load())
	request, err := runnerunit.ParseSubmission(wire)
	require.NoError(t, err)
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
	}}
	reuseWire, err := json.Marshal(request)
	require.NoError(t, err)
	reused := postSubmission(t, server, reuseWire, http.StatusOK)
	require.Equal(t, int32(1), calls.Load())
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, original.Terminal, reused.Terminal)
}

func TestPublicSubmissionHTTPBrowserFixtureUsesActualProducerBytes(t *testing.T) {
	directory := t.TempDir()
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: runnerunit.NewService(nil),
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	wire := checkpointOperationWire(t)
	original, originalJSON := postSubmissionBytes(t, server, wire, http.StatusOK)
	request, err := runnerunit.ParseSubmission(wire)
	require.NoError(t, err)
	request.Request = bytes.Replace(request.Request, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.Contains(t, string(request.Request), `"attempt":3`)
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
	}}
	reuseWire, err := json.Marshal(request)
	require.NoError(t, err)
	reused, reusedJSON := postSubmissionBytes(t, server, reuseWire, http.StatusOK)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, original.Terminal, reused.Terminal)
	fixture, err := json.Marshal(struct {
		Created string `json:"created"`
		Reused  string `json:"reused"`
	}{Created: string(originalJSON), Reused: string(reusedJSON)})
	require.NoError(t, err)
	if destination := os.Getenv("WAZA_SUBMISSION_BROWSER_FIXTURE"); destination != "" {
		output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		require.NoError(t, err)
		_, writeErr := output.Write(append(fixture, '\n'))
		require.NoError(t, errors.Join(writeErr, output.Close()))
	}
}
