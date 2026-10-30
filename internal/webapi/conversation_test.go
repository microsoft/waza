package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConversationSavedResultThroughHTTP(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "e2e", "fixtures", "conversation.json"))
	require.NoError(t, err)
	var fixture struct {
		Artifact                json.RawMessage `json:"artifact"`
		ExpectedTranscript      json.RawMessage `json:"expectedTranscript"`
		ExpectedEmptyTranscript json.RawMessage `json:"expectedEmptyTranscript"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Artifact)
	require.NotEmpty(t, fixture.ExpectedTranscript)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "conversation.json"), fixture.Artifact, 0o600))
	handler := NewHandlers(NewFileStore(dir))
	request := httptest.NewRequest(http.MethodGet, "/api/runs/conversation", nil)
	response := httptest.NewRecorder()
	handler.HandleRunDetail(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var detail RunDetail
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &detail))
	require.Len(t, detail.Tasks, 2)
	task := detail.Tasks[0]
	require.Equal(t, "multi-turn", task.ID)
	require.Equal(t, "Please review the deployment plan.", task.Prompt)
	require.NotNil(t, task.Responder)
	require.Equal(t, 2, task.Responder.FollowupsSent)
	require.Equal(t, "stopped", task.Responder.Outcome)
	require.Len(t, task.Transcript, 9)
	transcript, err := json.Marshal(task.Transcript)
	require.NoError(t, err)
	require.JSONEq(t, string(fixture.ExpectedTranscript), string(transcript))
	require.NotContains(t, string(transcript), "SECOND_REPETITION")
	emptyTask := detail.Tasks[1]
	require.Equal(t, "empty-messages", emptyTask.ID)
	require.Len(t, emptyTask.Transcript, 2)
	emptyTranscript, err := json.Marshal(emptyTask.Transcript)
	require.NoError(t, err)
	require.JSONEq(t, string(fixture.ExpectedEmptyTranscript), string(emptyTranscript))
}
