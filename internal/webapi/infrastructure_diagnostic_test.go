package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInfrastructureDiagnosticsProjection(t *testing.T) {
	const legacy = `{"run_id":"eval","tasks":[{"test_id":"task","status":"error","runs":[{"run_number":1,"attempts":1,"status":"passed"},{"run_number":2,"attempts":2,"status":"error","error_msg":"/private/secret-token"},{"run_number":3,"attempts":3,"status":"error","error_msg":"secret-token","infrastructure_diagnostic":{"code":"workspace_capture_file_bytes","limit":16777216}},{"run_number":4,"attempts":1,"status":"error","infrastructure_diagnostic":{"code":"/private/secret-token","limit":9999999999999}}]}]}`
	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal([]byte(legacy), &outcome))
	task := outcomeToDetail(&outcome).Tasks[0]
	require.Empty(t, task.GraderResults)
	require.Len(t, task.InfrastructureRuns, 4)
	require.Nil(t, task.InfrastructureRuns[0].Diagnostic)
	require.Equal(t, 1, task.InfrastructureRuns[0].RunNumber)
	require.Equal(t, 2, task.InfrastructureRuns[1].Attempts)
	require.Equal(t, models.CaptureFailed, task.InfrastructureRuns[1].Diagnostic.Code)
	require.Equal(t, models.CaptureFileBytes, task.InfrastructureRuns[2].Diagnostic.Code)
	require.EqualValues(t, 16777216, task.InfrastructureRuns[2].Diagnostic.Limit)
	require.Equal(t, models.CaptureFailed, task.InfrastructureRuns[3].Diagnostic.Code)
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-token")
	require.NotContains(t, string(raw), "/private")
	require.NotContains(t, string(raw), "9999999999999")
	artifact, err := json.Marshal(outcome)
	require.NoError(t, err)
	require.Contains(t, string(artifact), "/private/secret-token", "CLI error_msg remains unchanged")
	require.NotContains(t, string(artifact), `"code":"/private/secret-token"`)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.json"), []byte(legacy), 0o600))
	response := httptest.NewRecorder()
	NewHandlers(NewFileStore(dir)).HandleRunDetail(response, httptest.NewRequest(http.MethodGet, "/api/runs/eval", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "secret-token")
	require.NotContains(t, response.Body.String(), "/private")
	require.JSONEq(t, string(raw), taskJSONFromDetail(t, response.Body.Bytes()))
}

func taskJSONFromDetail(t *testing.T, data []byte) string {
	t.Helper()
	var detail struct {
		Tasks []json.RawMessage `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &detail))
	require.Len(t, detail.Tasks, 1)
	return string(detail.Tasks[0])
}

func TestInfrastructureDiagnosticBoundaries(t *testing.T) {
	for _, limit := range []int64{-1, 0, 1<<40 + 1} {
		d := models.InfrastructureDiagnostic{Code: models.CaptureFileBytes, Limit: limit}
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "limit")
	}
}
