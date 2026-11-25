package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
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
	require.Equal(t, models.InfrastructureError, task.InfrastructureRuns[1].Diagnostic.Code)
	require.Equal(t, models.CaptureFileBytes, task.InfrastructureRuns[2].Diagnostic.Code)
	require.EqualValues(t, 16777216, task.InfrastructureRuns[2].Diagnostic.Limit)
	require.Equal(t, models.InfrastructureError, task.InfrastructureRuns[3].Diagnostic.Code)
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

func TestResponderReasonsAreNeverProjected(t *testing.T) {
	for _, outcome := range []string{
		models.ResponderOutcomeError, models.ResponderOutcomeAbstained,
		models.ResponderOutcomeStopped, models.ResponderOutcomeCapExhausted, "/private/secret-token",
	} {
		t.Run(outcome, func(t *testing.T) {
			original := models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{{
				Responder: &models.ResponderInfo{Outcome: outcome, Reason: "/private/secret-token"},
			}}}}}
			data, err := json.Marshal(outcomeToDetail(&original))
			require.NoError(t, err)
			require.NotContains(t, string(data), "/private/secret-token")
			artifact, err := json.Marshal(original)
			require.NoError(t, err)
			require.Contains(t, string(artifact), "/private/secret-token")
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.json"), artifact, 0o600))
			response := httptest.NewRecorder()
			NewHandlers(NewFileStore(dir)).HandleRunDetail(response, httptest.NewRequest(http.MethodGet, "/api/runs/eval", nil))
			require.Equal(t, http.StatusOK, response.Code)
			require.NotContains(t, response.Body.String(), "/private/secret-token")
		})
	}
}

func TestUserInputErrorsAreNeverProjected(t *testing.T) {
	records := []models.UserInputRecord{
		{Sequence: 1, Question: "first", Error: "native input abstained: /private/secret-token"},
		{Sequence: 2, Question: "second", Error: "another arbitrary secret-value"},
		{Sequence: 3, Question: "third"},
	}
	original := models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{{
		UserInputRequests: &records,
	}}}}}
	artifact, err := json.Marshal(original)
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.json"), artifact, 0o600))
	response := httptest.NewRecorder()
	NewHandlers(NewFileStore(dir)).HandleRunDetail(response, httptest.NewRequest(http.MethodGet, "/api/runs/eval", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "/private")
	require.NotContains(t, response.Body.String(), "secret-token")
	require.NotContains(t, response.Body.String(), "secret-value")
	var detail RunDetail
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	projected := *detail.Tasks[0].UserInputRequests
	require.Len(t, projected, 3)
	require.Equal(t, "A user input error occurred.", projected[0].Error)
	require.Equal(t, "A user input error occurred.", projected[1].Error)
	require.Empty(t, projected[2].Error)
	require.Equal(t, 3, projected[2].Sequence)
	require.Equal(t, "native input abstained: /private/secret-token", records[0].Error)
	stored, err := os.ReadFile(filepath.Join(dir, "eval.json"))
	require.NoError(t, err)
	require.Equal(t, artifact, stored)
}

func TestSessionErrorsAreNeverProjected(t *testing.T) {
	const artifact = `{"run_id":"eval","tasks":[{"test_id":"task","status":"error","runs":[{"run_number":1,"status":"error","error_msg":"/private/execution-secret","responder":{"outcome":"error","reason":"/private/responder-secret"},"infrastructure_diagnostic":{"code":"workspace_capture_file_bytes","limit":16,"additional_failure":true},"transcript":[{"type":"assistant.message","content":"Ordinary assistant response"},{"type":"session.error","message":"follow-up failed: /private/transcript-secret"},{"type":"session.error","message":"another arbitrary token-123"}],"session_digest":{"errors":["/private/digest-secret","token-456",""]}}]}]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.json")
	require.NoError(t, os.WriteFile(path, []byte(artifact), 0o600))
	response := httptest.NewRecorder()
	NewHandlers(NewFileStore(dir)).HandleRunDetail(response, httptest.NewRequest(http.MethodGet, "/api/runs/eval", nil))
	require.Equal(t, http.StatusOK, response.Code)
	for _, private := range []string{"/private", "transcript-secret", "digest-secret", "token-123", "token-456"} {
		require.NotContains(t, response.Body.String(), private)
	}
	var detail RunDetail
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	task := detail.Tasks[0]
	require.Equal(t, "Ordinary assistant response", task.Transcript[0].Content)
	require.Equal(t, "A session error occurred.", task.Transcript[1].Message)
	require.Equal(t, "A session error occurred.", task.Transcript[2].Message)
	require.Equal(t, []string{"A session error occurred.", "A session error occurred.", ""}, task.SessionDigest.Errors)
	require.True(t, task.InfrastructureRuns[0].Diagnostic.AdditionalFailure)
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, artifact, string(stored))

	rawError := &copilot.SessionErrorData{Message: "/private/modern-secret"}
	modern := models.TranscriptEvent{SessionEvent: copilot.SessionEvent{Data: rawError}}
	projected := mapTranscriptEvents([]models.TranscriptEvent{modern})
	require.Equal(t, "A session error occurred.", projected[0].Message)
	require.Equal(t, "/private/modern-secret", rawError.Message)
}

func TestLegacyAttemptIsOmitted(t *testing.T) {
	outcome := models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{{
		RunNumber: 7, Status: models.StatusError, ErrorMsg: "private",
	}}}}}
	data, err := json.Marshal(outcomeToDetail(&outcome))
	require.NoError(t, err)
	require.Contains(t, string(data), `"runNumber":7`)
	require.NotContains(t, string(data), `"attempts"`)
	require.Contains(t, string(data), `"code":"infrastructure_error"`)
}

func TestPromptJudgingFixtureInfrastructureProjection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "e2e", "fixtures", "prompt-judging.json"))
	require.NoError(t, err)
	var fixture RunDetail
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Len(t, fixture.Tasks, 1)
	outcome := models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{
		{RunNumber: 1, Attempts: 1, Status: models.StatusFailed},
		{RunNumber: 2, Attempts: 1, Status: models.StatusError},
		{RunNumber: 3, Attempts: 1, Status: models.StatusPassed},
	}}}}
	require.Equal(t, outcomeToDetail(&outcome).Tasks[0].InfrastructureRuns, fixture.Tasks[0].InfrastructureRuns)
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
