package webapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func submissionServer(t *testing.T) *httptest.Server {
	t.Helper()
	directory := t.TempDir()
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{
		open: func() (artifactmanifest.Backend, func() error, error) {
			return artifactmanifest.OpenBackend(directory, "")
		},
		runner: runnerunit.NewService(nil),
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func postSubmission(t *testing.T, server *httptest.Server, wire []byte, status int) runnerunit.Submission {
	t.Helper()
	response, err := server.Client().Post(server.URL+"/api/v1/submissions", "application/json", bytes.NewReader(wire))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, status, response.StatusCode, "%s", body)
	result, err := runnerunit.ParseSubmissionResult(body)
	require.NoError(t, err, "%s", body)
	require.Equal(t, strconv.Itoa(result.ExitCode), response.Header.Get("X-Waza-Exit-Code"))
	return *result
}

func getLineage(t *testing.T, server *httptest.Server, logical executionattempt.Logical, status int) []byte {
	t.Helper()
	query := url.Values{"run": {logical.Run}, "work": {logical.Work}}
	response, err := server.Client().Get(server.URL + "/api/v1/lineage?" + query.Encode())
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, status, response.StatusCode, "%s", body)
	return body
}

func TestSubmissionHTTPRetryReuseAndLineage(t *testing.T) {
	server := submissionServer(t)
	wire := checkpointOperationWire(t)
	first := postSubmission(t, server, wire, http.StatusOK)
	require.Equal(t, "completed", first.Status)

	var request runnerunit.SubmissionRequest
	require.NoError(t, json.Unmarshal(wire, &request))
	request.Request = bytes.Replace(request.Request, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct",
		Parent: &executionattempt.Parent{ExecutionID: first.Reservation.ExecutionID, Relationship: "retry"}}
	retryWire, err := json.Marshal(request)
	require.NoError(t, err)
	retry := postSubmission(t, server, retryWire, http.StatusOK)
	require.Equal(t, "completed", retry.Status)

	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest}}
	reuseWire, err := json.Marshal(request)
	require.NoError(t, err)
	reused := postSubmission(t, server, reuseWire, http.StatusOK)
	require.Equal(t, "reused", reused.Status)
	require.Zero(t, reused.EngineCalls)

	lineage, err := executionledger.ParseLineage(getLineage(t, server, first.Reservation.Logical, http.StatusOK))
	require.NoError(t, err)
	require.Len(t, lineage.Attempts, 2)
	require.Equal(t, retry.Reservation.ExecutionID, lineage.Attempts[1].ExecutionID)
	require.Equal(t, []string{reused.Decision.DecisionID}, lineage.Attempts[0].ReusedBy)
	require.Empty(t, lineage.Diagnostics)
}

func TestSubmissionHTTPRejectsInvalidInputWithoutEcho(t *testing.T) {
	server := submissionServer(t)
	result := postSubmission(t, server, []byte(`{"secret-control":1}`), http.StatusBadRequest)
	require.Equal(t, "rejected", result.Status)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-control")

	body := getLineage(t, server, executionattempt.Logical{Run: "run-x", Work: "trial-secret-control"}, http.StatusBadRequest)
	require.NotContains(t, string(body), "secret-control")
}

func TestLineageHTTPRequiresLoopbackSameOrigin(t *testing.T) {
	mux := http.NewServeMux()
	registerSubmissionRoutes(mux, submissionOperations{open: func() (artifactmanifest.Backend, func() error, error) {
		t.Fatal("storage must not open before authorization")
		return nil, nil, nil
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/lineage", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}
