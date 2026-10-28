package webapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerHTTPAndServiceParity(t *testing.T) {
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	var req runnercontract.Request
	require.NoError(t, json.Unmarshal(data, &req))
	for _, name := range []string{"success", "unsupported", "invalid-selection", "evaluation-failure", "malformed"} {
		t.Run(name, func(t *testing.T) {
			request := req
			switch name {
			case "unsupported":
				request.RequiredCapabilities = []string{"unavailable"}
			case "invalid-selection":
				request.Selection.Task = "adjacent"
			case "evaluation-failure":
				request.Input.Task += "  output_not_contains:\n    - hello\n"
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			if name == "malformed" {
				body = []byte("{")
			}
			mux := http.NewServeMux()
			resultsDir := t.TempDir()
			RegisterRunnerRoutes(mux, resultsDir)
			w := httptest.NewRecorder()
			httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(body))
			httpRequest.Host = "localhost"
			httpRequest.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(w, httpRequest)
			require.Equal(t, http.StatusOK, w.Code)
			actual, err := runnercontract.ParseResult(w.Body.Bytes())
			require.NoError(t, err)
			expected := runnercontract.NewService().Run(t.Context(), body)
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				require.Equal(t, runnercontract.InfrastructureFailure, actual.Outcome)
				require.Equal(t, 2, actual.ExitCode)
				require.Equal(t, expected.Identity, actual.Identity)
				require.Equal(t, "publication_failed", actual.Diagnostics[len(actual.Diagnostics)-1].Code)
				require.Contains(t, actual.Diagnostics[len(actual.Diagnostics)-1].Message, "unsupported on this platform")
				return
			}
			require.Equal(t, expected.Outcome, actual.Outcome)
			require.Equal(t, expected.ExitCode, actual.ExitCode)
			require.Equal(t, expected.Negotiation, actual.Negotiation)
			require.Equal(t, expected.Identity, actual.Identity)
			require.Equal(t, expected.Resolved, actual.Resolved)
			require.Equal(t, expected.Diagnostics, actual.Diagnostics)
			saved, err := readRunnerResults(resultsDir)
			require.NoError(t, err)
			require.Len(t, saved, 1, "retain admission/negotiation failures as well as executed units")
			require.Equal(t, *actual, saved[0])
		})
	}
}

func TestRunnerLabArtifactContract(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	artifact, err := json.Marshal(result)
	require.NoError(t, err)
	path := filepath.Join(dir, "unit.runner.json")
	require.NoError(t, os.WriteFile(path, artifact, 0o600))
	legacyStore := NewFileStore(dir)
	legacy, err := legacyStore.ListRuns("", "")
	require.NoError(t, err)
	require.Empty(t, legacy, "runner envelopes must not become synthetic historical evaluations")
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runner-results", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response runnerArchivePage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "1.1", response.SchemaVersion)
	require.Len(t, response.Results, 1)
	require.JSONEq(t, string(artifact), string(response.Results[0].Result))
	require.NoError(t, os.WriteFile(path, []byte(`{"schema_version":"1.0"}`), 0o600))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runner-results", nil))
	require.Equal(t, http.StatusOK, w.Code)
	response = runnerArchivePage{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Results, 1)
	require.NotEmpty(t, response.Results[0].Error)
	require.Empty(t, response.Results[0].Result)
	require.NotContains(t, w.Body.String(), `"outcome":"success"`, "invalid refresh must not retain stale success")
}

func TestRunnerCapabilityAdvertisement(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, t.TempDir())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runner/capabilities", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "1.0", response["schema_version"])
	require.Contains(t, response["capabilities"], "inline.mock-text")
}

func TestRunnerHTTPRejectsCrossOriginAndNonJSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, t.TempDir())
	for _, origin := range []string{"https://unrelated.example", "null", ""} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader([]byte("{}")))
		request.Host = "localhost"
		if origin != "" {
			request.Header.Set("Origin", origin)
			request.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, request)
		result, err := runnercontract.ParseResult(w.Body.Bytes())
		require.NoError(t, err)
		require.Equal(t, runnercontract.InvalidRequest, result.Outcome)
		require.Nil(t, result.Identity)
	}
}

func TestRunnerHTTPRejectsDNSAliasesBeforeAdmission(t *testing.T) {
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, t.TempDir())
	for _, host := range []string{"localhost:3000", "127.0.0.1:3000", "[::1]:3000", "rebound.example:3000"} {
		t.Run(host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
			request.Host = host
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "http://"+host)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, request)
			result, err := runnercontract.ParseResult(w.Body.Bytes())
			require.NoError(t, err)
			if host == "rebound.example:3000" {
				require.Equal(t, runnercontract.InvalidRequest, result.Outcome)
				require.Nil(t, result.Identity)
				require.Contains(t, result.Diagnostics[0].Message, "loopback Host")
			} else {
				if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
					require.Equal(t, runnercontract.Success, result.Outcome)
				} else {
					require.Equal(t, runnercontract.InfrastructureFailure, result.Outcome)
					require.NotNil(t, result.Observation)
					require.Equal(t, "publication_failed", result.Diagnostics[len(result.Diagnostics)-1].Code)
				}
			}
		})
	}
}
