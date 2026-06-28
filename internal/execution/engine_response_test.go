package execution

import (
	"testing"

	"github.com/microsoft/waza/internal/agentevents"
	"github.com/stretchr/testify/assert"
)

func TestExecutionResponse_ExtractMessages(t *testing.T) {
	hello := "hello"
	world := "world"
	ignoredDelta := "delta"

	resp := &ExecutionResponse{
		Events: []agentevents.Event{
			{Data: &agentevents.AssistantMessageData{Content: hello}},
			{Data: &agentevents.AssistantMessageData{}},
			{Data: &agentevents.AssistantMessageDeltaData{DeltaContent: ignoredDelta}},
			{Data: &agentevents.AssistantMessageData{Content: world}},
		},
	}

	assert.Equal(t, []string{"hello", "world"}, resp.ExtractMessages())
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
