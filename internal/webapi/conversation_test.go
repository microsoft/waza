package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
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

	var saved models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(fixture.Artifact, &saved))
	require.Len(t, saved.TestOutcomes, 2)
	require.Equal(t, "empty-messages", saved.TestOutcomes[1].TestID)
	require.Len(t, saved.TestOutcomes[1].Runs, 2)
	secondTranscript := saved.TestOutcomes[1].Runs[1].Transcript
	require.Len(t, secondTranscript, 2)
	userText, ok := copilotevents.Content(secondTranscript[0].SessionEvent)
	require.True(t, ok)
	require.Equal(t, "SECOND_REPETITION_HAS_USER_TEXT", userText)
	assistantText, ok := copilotevents.Content(secondTranscript[1].SessionEvent)
	require.True(t, ok)
	require.Equal(t, "SECOND_REPETITION_HAS_ASSISTANT_TEXT", assistantText)

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
	require.Len(t, emptyTask.Transcript, 8)
	emptyTranscript, err := json.Marshal(emptyTask.Transcript)
	require.NoError(t, err)
	require.JSONEq(t, string(fixture.ExpectedEmptyTranscript), string(emptyTranscript))
	require.NotContains(t, string(emptyTranscript), "SECOND_REPETITION")
}
