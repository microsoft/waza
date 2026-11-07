package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestAggregateFrozenManifestOnlyCLIAPICompatibility(t *testing.T) {
	data, err := os.ReadFile("../../internal/manifestaggregate/testdata/manifest-only-ec565.json")
	require.NoError(t, err)
	var snapshot struct {
		Baseline string `json:"baseline_commit"`
		Controls []struct {
			Name       string                    `json:"name"`
			Objects    map[string][]byte         `json:"objects"`
			Request    manifestaggregate.Request `json:"request"`
			Report     []byte                    `json:"report"`
			CSV        []byte                    `json:"csv"`
			Incomplete bool                      `json:"incomplete"`
		} `json:"controls"`
	}
	require.NoError(t, json.Unmarshal(data, &snapshot))
	require.Equal(t, "ec565056c693d7a069eca9a66095c060f7187460", snapshot.Baseline)
	require.Len(t, snapshot.Controls, 4)
	names := make([]string, 0, len(snapshot.Controls))
	for _, control := range snapshot.Controls {
		names = append(names, control.Name)
	}
	require.Equal(t, []string{"success-and-evaluation-failure", "recorded-empty", "absent-observation", "missing"}, names)
	binary := buildRecoveryCLI(t)
	for _, control := range snapshot.Controls {
		t.Run(control.Name, func(t *testing.T) {
			require.Nil(t, control.Request.Evidence)
			require.NotEmpty(t, control.Report)
			require.NotEmpty(t, control.CSV)
			var expected manifestaggregate.Report
			require.NoError(t, json.Unmarshal(control.Report, &expected))
			require.Nil(t, expected.Evidence)
			require.NotEmpty(t, expected.Members)
			require.Equal(t, control.Incomplete, expected.Completion == "incomplete")
			directory := t.TempDir()
			storeDirectory := filepath.Join(directory, ".waza-artifacts")
			require.NoError(t, os.Mkdir(storeDirectory, 0o700))
			backend, err := artifactmanifest.OpenLocal(storeDirectory)
			require.NoError(t, err)
			for _, key := range slices.Sorted(maps.Keys(control.Objects)) {
				created, err := backend.Create(t.Context(), key, control.Objects[key])
				require.NoError(t, err)
				require.True(t, created)
			}
			require.NoError(t, backend.Close())
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutes(mux, directory, nil)
			manifests := slices.Clone(control.Request.Manifests)
			slices.Reverse(manifests)
			manifests = append(manifests, control.Request.Manifests...)
			body, err := json.Marshal(manifestaggregate.Request{Manifests: manifests})
			require.NoError(t, err)
			status, exitCode := http.StatusOK, ExitSuccess
			if control.Incomplete {
				status, exitCode = http.StatusConflict, ExitError
			} else if control.Name == "success-and-evaluation-failure" {
				exitCode = ExitTestFailed
			}
			for _, format := range []string{"json", "csv"} {
				frozen := control.Report
				endpoint := "/api/v1/aggregates"
				if format == "csv" {
					frozen = control.CSV
					endpoint += "?format=csv"
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body)))
				require.Equal(t, status, response.Code)
				require.Equal(t, frozen, response.Body.Bytes(), "API must retain independently frozen baseline bytes")
				command := exec.CommandContext(t.Context(), binary,
					append([]string{"aggregate", "--store", storeDirectory, "--format", format}, manifests...)...)
				command.Dir = directory
				command.Env = append(os.Environ(), "NO_COLOR=1", "ENABLE_COPILOT_TESTS=false",
					"ENABLE_LIVE_COPILOT_TESTS=false", "COPILOT_CLI_PATH=/usr/bin/false", "WAZA_NO_UPDATE_CHECK=1")
				var output bytes.Buffer
				command.Stdout = &output
				runErr := command.Run()
				if exitCode == ExitSuccess {
					require.NoError(t, runErr)
				} else {
					var failure *exec.ExitError
					require.ErrorAs(t, runErr, &failure, "expected completed CLI exit policy, not setup failure")
					require.Equal(t, exitCode, failure.ExitCode())
				}
				actual := output.Bytes()
				if format == "json" {
					actual = bytes.TrimSuffix(actual, []byte("\n"))
				}
				require.Equal(t, frozen, actual, "CLI must retain independently frozen baseline bytes")
			}
		})
	}
}
