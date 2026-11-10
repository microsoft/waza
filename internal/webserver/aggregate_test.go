package webserver

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

var aggregateProductionFixture = flag.String("aggregate-production-fixture-output", "", "Exclusively create public sources and canonical API output for the embedded Lab browser control")

func TestAggregateProductionConfiguredSourceAndBrowserFixture(t *testing.T) {
	directory := t.TempDir()
	if *aggregateProductionFixture != "" {
		directory = *aggregateProductionFixture
		require.True(t, filepath.IsAbs(directory))
		require.NoError(t, os.Mkdir(directory, 0o700))
	}
	results := filepath.Join(directory, "results")
	store := filepath.Join(results, ".waza-artifacts")
	require.NoError(t, os.MkdirAll(store, 0o700))
	backend, err := artifactmanifest.OpenLocal(store)
	require.NoError(t, err)
	source := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedNonzero(1))
	require.NoError(t, backend.Close())
	inputs := []manifestaggregate.Input{{Manifest: source.Manifest, Evidence: source.Evidence}}
	wire, err := json.Marshal(inputs)
	require.NoError(t, err)
	// A different working directory must not alter either public capability.
	t.Chdir(t.TempDir())
	server, err := New(Config{ResultsDir: results, NoBrowser: true, Lab: true})
	require.NoError(t, err)
	call := func(body []byte) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(body)))
		return response
	}
	response := call(wire)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	report, err := manifestaggregate.Parse(response.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, 1, report.Counts.VerifiedExecutions, "configured namespace must verify the original execution")
	require.Equal(t, source.Execution, report.Members[0].Execution)
	require.Equal(t, "complete", report.Usage.State)
	require.Equal(t, 1.25, *report.Usage.Recorded.AICredits)
	canonical := bytes.Clone(response.Body.Bytes())
	for _, control := range []struct {
		name   string
		config *projectconfig.StorageConfig
	}{
		{"active-local", &projectconfig.StorageConfig{Enabled: true, Provider: "local"}},
		{"disabled-remote", &projectconfig.StorageConfig{Enabled: false, Provider: "azure-blob"}},
	} {
		t.Run(control.name, func(t *testing.T) {
			configured, err := New(Config{ResultsDir: results, NoBrowser: true, StorageConfig: control.config})
			require.NoError(t, err)
			response := httptest.NewRecorder()
			configured.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(wire)))
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, canonical, response.Body.Bytes())
		})
	}
	t.Run("unsupported-active-provider-cannot-use-valid-local-neighbor", func(t *testing.T) {
		configured, err := New(Config{ResultsDir: results, NoBrowser: true, StorageConfig: &projectconfig.StorageConfig{
			Enabled: true, Provider: "unsupported",
		}})
		require.NoError(t, err)
		response := httptest.NewRecorder()
		configured.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(wire)))
		require.Equal(t, http.StatusServiceUnavailable, response.Code)
		require.JSONEq(t, `{"error":"aggregate_store_unavailable"}`, response.Body.String())
	})
	inputs = append(inputs, manifestaggregate.Input{Manifest: strings.Repeat("a", 64)})
	incomplete, err := json.Marshal(inputs)
	require.NoError(t, err)
	response = call(incomplete)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	report, err = manifestaggregate.Parse(response.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "incomplete", report.State)
	require.Equal(t, 2, report.Counts.Members)
	require.Equal(t, 1, report.Counts.VerifiedExecutions)
	require.Nil(t, report.Usage.Recorded)
	response = call([]byte(`[]`))
	require.Equal(t, http.StatusBadRequest, response.Code)
	if *aggregateProductionFixture != "" {
		require.NoError(t, os.WriteFile(filepath.Join(directory, "inputs.json"), wire, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "canonical.json"), canonical, 0o600))
	}
}

func TestAggregateProductionMissingStoreDoesNotFallBackToWorkingDirectory(t *testing.T) {
	working := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(working, ".waza-artifacts"), 0o700))
	t.Chdir(working)
	selected := t.TempDir()
	server, err := New(Config{ResultsDir: selected, NoBrowser: true})
	require.NoError(t, err)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates",
		strings.NewReader(`[{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}]`)))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"error":"aggregate_store_unavailable"}`, response.Body.String())
	require.NoDirExists(t, filepath.Join(selected, ".waza-artifacts"))
}

func TestAggregateConfiguredProviderDenialBeforeOpeningOrCredentials(t *testing.T) {
	for _, provider := range []string{"azure-blob", "unsupported"} {
		t.Run(provider, func(t *testing.T) {
			directory := t.TempDir()
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutes(mux, directory, &projectconfig.StorageConfig{
				Enabled: true, Provider: provider,
			})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates",
				strings.NewReader(`[{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}]`)))
			require.Equal(t, http.StatusServiceUnavailable, response.Code)
			require.JSONEq(t, `{"error":"aggregate_store_unavailable"}`, response.Body.String())
			require.NoDirExists(t, filepath.Join(directory, ".waza-artifacts"))
		})
	}
}
