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
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

type checkpointBrowserFixture struct {
	Path       string          `json:"path"`
	Request    json.RawMessage `json:"request"`
	Admission  json.RawMessage `json:"admission"`
	HTTPStatus int             `json:"http_status"`
	Response   string          `json:"response"`
}

func TestCheckpointBrowserFixturesHaveActualProtectedHTTPProducers(t *testing.T) {
	directory := t.TempDir()
	var revision atomic.Pointer[string]
	var failFinal atomic.Bool
	var failBefore atomic.Bool
	initialRevision, changedRevision := strings.Repeat("c", 40), strings.Repeat("d", 40)
	revision.Store(&initialRevision)
	mux := http.NewServeMux()
	service := runnerunit.NewService(nil)
	RegisterRunnerRoutesWithService(mux, service)
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: service, revision: func() (string, error) { return *revision.Load(), nil },
		observers: runnercheckpoint.Hooks{Published: func(_ context.Context, reference runnercheckpoint.Reference) error {
			if failBefore.Load() && reference.Boundary == executioncheckpoint.BeforeUnit {
				return errors.New("fixture stops the original worker before dispatch")
			}
			if failFinal.Load() && reference.Boundary == executioncheckpoint.FinalizedUnit {
				return errors.New("fixture rejects finalized progress delivery after actual completed execution")
			}
			return nil
		}},
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	post := func(path string, wire []byte, expectedStatus int) []byte {
		response, err := server.Client().Post(server.URL+path, "application/json", bytes.NewReader(wire))
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, expectedStatus, response.StatusCode, "%s", body)
		if strings.HasPrefix(path, "/api/v1/checkpoints") {
			result, err := runnercheckpoint.ParseResult(body)
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(result.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
		}
		return body
	}
	fixtures := map[string]checkpointBrowserFixture{}
	record := func(name, path string, wire []byte, status int) runnercheckpoint.Result {
		body := post(path, wire, status)
		result, err := runnercheckpoint.ParseResult(body)
		require.NoError(t, err)
		require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
		request, err := runnerunit.ParseSubmission(wire)
		require.NoError(t, err)
		admission := post("/api/v1/runner/validate", request.Request, http.StatusOK)
		fixtures[name] = checkpointBrowserFixture{
			Path: path, Request: wire, Admission: admission, HTTPStatus: status, Response: string(body),
		}
		return *result
	}
	wire := checkpointOperationWire(t)
	success := record("created", "/api/v1/checkpoints", wire, http.StatusOK)
	require.Equal(t, "completed", success.Status)
	require.Equal(t, 1, success.Submission.EngineCalls)

	failedWire := bytes.Replace(wire, []byte("- hello"), []byte("- missing-browser-control"), 1)
	require.NotEqual(t, wire, failedWire)
	failed := record("failed", "/api/v1/checkpoints", failedWire, http.StatusOK)
	require.Equal(t, 1, failed.ExitCode)
	destination := bytes.Replace(failedWire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, failedWire, destination)
	reused := record("reused", "/api/v1/checkpoints/"+failed.FinalizedDigest+"/resume", destination, http.StatusOK)
	require.Equal(t, "reused", reused.Status)
	require.Zero(t, reused.Submission.EngineCalls)
	require.Equal(t, 1, reused.ExitCode)
	require.Equal(t, failed.Submission.Terminal, reused.Submission.Terminal)

	failBefore.Store(true)
	paused, err := runnercheckpoint.ParseResult(post("/api/v1/checkpoints", wire, http.StatusInternalServerError))
	require.NoError(t, err)
	require.NotEmpty(t, paused.BeforeDigest)
	require.Zero(t, paused.Submission.EngineCalls)
	require.Nil(t, paused.Submission.Completion)
	failBefore.Store(false)
	retryWire := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, wire, retryWire)
	retried := record("retried", "/api/v1/checkpoints/"+paused.BeforeDigest+"/resume", retryWire, http.StatusOK)
	require.Equal(t, "completed", retried.Status)
	require.Equal(t, executioncheckpoint.BeforeUnit, retried.Source.Boundary)
	require.NotEqual(t, paused.Submission.Reservation.ExecutionID, retried.Submission.Reservation.ExecutionID)
	require.Equal(t, paused.Submission.Reservation.ExecutionID, retried.Submission.Reservation.Parent.ExecutionID)
	require.Equal(t, "retry", retried.Submission.Reservation.Parent.Relationship)
	require.Equal(t, 1, retried.Submission.EngineCalls)
	require.Equal(t, 3, retried.Submission.Terminal.Selection.Attempt)

	revision.Store(&changedRevision)
	incompatible := record("incompatible", "/api/v1/checkpoints/"+failed.FinalizedDigest+"/resume", destination, http.StatusConflict)
	require.True(t, incompatible.SourceWithheld)
	require.Nil(t, incompatible.Source)
	require.Nil(t, incompatible.RestoredUnits)
	require.Nil(t, incompatible.RemainingWork)
	revision.Store(&initialRevision)
	stale := record("stale", "/api/v1/checkpoints/"+success.BeforeDigest+"/resume", wire, http.StatusConflict)
	require.True(t, stale.SourceWithheld)
	require.Nil(t, stale.Submission)
	failFinal.Store(true)
	publicationFailure := record("checkpoint_error", "/api/v1/checkpoints", wire, http.StatusInternalServerError)
	require.Equal(t, "checkpoint_error", publicationFailure.Status)
	require.Equal(t, 2, publicationFailure.ExitCode)
	require.Equal(t, "completed", publicationFailure.Submission.Status)
	require.Zero(t, publicationFailure.Submission.ExitCode)
	require.NotNil(t, publicationFailure.Submission.Completion)

	path := filepath.Join("..", "..", "web", "e2e", "fixtures", "checkpoint-operations.json")
	if os.Getenv("UPDATE_CHECKPOINT_BROWSER_FIXTURES") == "1" {
		data, err := json.MarshalIndent(fixtures, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err, "generate actual HTTP fixtures with UPDATE_CHECKPOINT_BROWSER_FIXTURES=1")
	var saved map[string]checkpointBrowserFixture
	require.NoError(t, json.Unmarshal(data, &saved))
	require.Len(t, saved, len(fixtures))
	for name, current := range fixtures {
		t.Run(name, func(t *testing.T) {
			original, exists := saved[name]
			require.True(t, exists)
			require.Equal(t, strings.HasSuffix(current.Path, "/resume"), strings.HasSuffix(original.Path, "/resume"))
			require.JSONEq(t, string(current.Request), string(original.Request))
			require.JSONEq(t, string(current.Admission), string(original.Admission))
			require.Equal(t, current.HTTPStatus, original.HTTPStatus)
			actual, err := runnercheckpoint.ParseResult([]byte(current.Response))
			require.NoError(t, err)
			expected, err := runnercheckpoint.ParseResult([]byte(original.Response))
			require.NoError(t, err)
			require.Equal(t, checkpointBrowserValues(*expected), checkpointBrowserValues(*actual))
		})
	}
}

// IDs, content-addressed references and creation times vary with fresh owned
// reservations. Strict codecs admit each unmodified response before comparison;
// this projection compares rendered presence, original terminal and diagnostics,
// not normalized objects presented as verified execution evidence.
func checkpointBrowserValues(result runnercheckpoint.Result) map[string]any {
	values := map[string]any{
		"status": result.Status, "exit": result.ExitCode,
		"withheld": result.SourceWithheld, "source": result.Source != nil,
		"compatibility": result.CompatibilityResult, "differences": result.CompatibilityDifferences,
		"restored": result.RestoredUnits, "remaining": result.RemainingWork, "diagnostics": result.Diagnostics,
		"before": result.BeforeDigest != "", "finalized": result.FinalizedDigest != "",
		"submission": result.Submission != nil,
	}
	if result.Source != nil {
		values["boundary"] = result.Source.Boundary
	}
	if submission := result.Submission; submission != nil {
		values["calls"] = submission.EngineCalls
		values["terminal"] = submission.Terminal
		values["report"] = len(submission.Report)
		values["submission_diagnostics"] = submission.Diagnostics
		values["caller"] = submission.Caller
		values["reservation"] = submission.Reservation != nil
		values["completion"] = submission.Completion != nil
		values["decision"] = submission.Decision != nil
		if submission.Reservation != nil {
			values["selection"] = submission.Reservation.Binding.Selection
			values["parent"] = submission.Reservation.Parent != nil
		}
		if submission.Decision != nil {
			values["policy"] = submission.Decision.Policy
			values["decision_outcome"] = submission.Decision.Outcome
		}
	}
	return values
}
