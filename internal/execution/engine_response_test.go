package execution

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
)

func TestExecutionResponse_ExtractMessages(t *testing.T) {
	hello := "hello"
	world := "world"
	ignoredDelta := "delta"

	resp := &ExecutionResponse{
		Events: []copilot.SessionEvent{
			{Data: &copilot.AssistantMessageData{Content: hello}},
			{Data: &copilot.AssistantMessageData{}},
			{Data: &copilot.AssistantMessageDeltaData{DeltaContent: ignoredDelta}},
			{Data: &copilot.AssistantMessageData{Content: world}},
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
