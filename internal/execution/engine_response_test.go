package execution

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestExecutionResponse_ExtractMessages(t *testing.T) {
	hello := "hello"
	world := "world"
	ignoredDelta := "delta"

	resp := &ExecutionResponse{
		Events: []models.SessionEvent{
			{EventType: models.SessionEventTypeAssistantMessage, Content: &hello},
			{EventType: models.SessionEventTypeAssistantMessage, Content: ptrString("")},
			{EventType: models.SessionEventTypeAssistantMessageDelta, DeltaContent: &ignoredDelta},
			{EventType: models.SessionEventTypeAssistantMessage, Content: &world},
		},
	}

	assert.Equal(t, []string{"hello", "world"}, resp.ExtractMessages())
}

func ptrString(value string) *string {
	return &value
}

func TestExecutionResponse_ContainsText(t *testing.T) {
	resp := &ExecutionResponse{FinalOutput: "The Quick Brown Fox"}

	assert.True(t, resp.ContainsText("quick brown"))
	assert.True(t, resp.ContainsText("FOX"))
	assert.False(t, resp.ContainsText("wolf"))
}

func TestContains(t *testing.T) {
	assert.True(t, contains("Hello", "he"))
	assert.True(t, contains("Hello", ""))
	assert.False(t, contains("Hello", "xyz"))
}
