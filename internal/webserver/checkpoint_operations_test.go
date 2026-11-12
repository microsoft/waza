package webserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestCheckpointProductionRouteAdmissionPrecedesStore(t *testing.T) {
	directory := t.TempDir()
	server, err := New(Config{ResultsDir: directory, NoBrowser: true})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server.srv.Handler)
	defer httpServer.Close()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	for _, control := range []struct {
		name           string
		wire           []byte
		schemaVersion  string
		status         string
		exitCode       int
		diagnosticCode string
		errorCode      string
		http           int
	}{
		{
			name: "invalid-wrapper", wire: []byte("{}"), schemaVersion: "1.0", status: "rejected",
			exitCode: 6, diagnosticCode: "checkpoint_input_invalid", http: http.StatusBadRequest,
		},
		{
			name: "not-owning-cli-build", wire: wire, schemaVersion: "1.1", status: "operation_error",
			exitCode: 2, errorCode: runnercheckpoint.ErrorCodeBuildUnavailable, http: http.StatusServiceUnavailable,
		},
	} {
		t.Run(control.name, func(t *testing.T) {
			response, err := httpServer.Client().Post(httpServer.URL+"/api/v1/checkpoints", "application/json", bytes.NewReader(control.wire))
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, control.http, response.StatusCode, "%s", body)
			result, err := runnercheckpoint.ParseResult(body)
			require.NoError(t, err)
			require.Equal(t, control.schemaVersion, result.SchemaVersion)
			require.Equal(t, control.status, result.Status)
			require.Equal(t, control.exitCode, result.ExitCode)
			require.Nil(t, result.Submission)
			if control.diagnosticCode != "" {
				require.Len(t, result.Diagnostics, 1)
				require.Equal(t, control.diagnosticCode, result.Diagnostics[0].Code)
			} else {
				require.Empty(t, result.Diagnostics)
				require.Equal(t, control.errorCode, result.ErrorCode)
			}
			require.NoDirExists(t, filepath.Join(directory, ".waza-artifacts"))
		})
	}
}
