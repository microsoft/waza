package execution_test

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"

	"github.com/microsoft/waza/internal/agentevent"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
)

// textPayload is a stand-in for a non-Copilot engine's event payload that
// carries text content via agentevent.TextProvider.
type textPayload struct{ content string }

func (t textPayload) Text() (string, bool) { return t.content, t.content != "" }

func TestExecutionResponse_ExtractMessages_CopilotEvents(t *testing.T) {
	resp := &execution.ExecutionResponse{
		Events: copilotevents.FromSDK([]copilot.SessionEvent{
			{Data: &copilot.AssistantMessageData{Content: "hello from copilot"}},
			{Data: &copilot.UserMessageData{Content: "user text — should be ignored"}},
			{Data: &copilot.AssistantMessageData{Content: ""}},
			{Data: &copilot.AssistantMessageData{Content: "second assistant reply"}},
		}),
	}

	got := resp.ExtractMessages()
	assert.Equal(t, []string{"hello from copilot", "second assistant reply"}, got)
}

func TestExecutionResponse_ExtractMessages_NonCopilotEngineFallback(t *testing.T) {
	// Simulate events emitted by a future non-Copilot engine: the payload
	// is not a copilot.SessionEvent, but it implements TextProvider so the
	// engine-neutral fallback in ExtractMessages should surface the text.
	resp := &execution.ExecutionResponse{
		Events: []agentevent.Event{
			agentevent.New(agentevent.KindAssistantMessage, textPayload{content: "claude says hi"}),
			agentevent.New(agentevent.KindUserMessage, textPayload{content: "user prompt"}),
			agentevent.New(agentevent.KindAssistantMessage, textPayload{content: ""}),
			agentevent.New(agentevent.KindAssistantMessage, textPayload{content: "follow-up"}),
		},
	}

	got := resp.ExtractMessages()
	assert.Equal(t, []string{"claude says hi", "follow-up"}, got)
}

func TestExecutionResponse_ExtractMessages_MixedEngines(t *testing.T) {
	// Mix Copilot-wrapped events with native non-Copilot events to ensure
	// both branches contribute to the result and ordering is preserved.
	copilotEvents := copilotevents.FromSDK([]copilot.SessionEvent{
		{Data: &copilot.AssistantMessageData{Content: "from copilot"}},
	})
	events := append([]agentevent.Event{}, copilotEvents...)
	events = append(events,
		agentevent.New(agentevent.KindAssistantMessage, textPayload{content: "from other engine"}),
	)

	resp := &execution.ExecutionResponse{Events: events}
	got := resp.ExtractMessages()
	assert.Equal(t, []string{"from copilot", "from other engine"}, got)
}

func TestExecutionResponse_ExtractMessages_NonCopilotWithoutTextProviderIsSkipped(t *testing.T) {
	// A non-Copilot payload that does NOT implement TextProvider must not
	// panic or surface anything — but unlike before, this is now an explicit
	// "no engine support" rather than a silent drop of Copilot data.
	type opaque struct{}
	resp := &execution.ExecutionResponse{
		Events: []agentevent.Event{
			agentevent.New(agentevent.KindAssistantMessage, opaque{}),
		},
	}
	assert.Empty(t, resp.ExtractMessages())
}
