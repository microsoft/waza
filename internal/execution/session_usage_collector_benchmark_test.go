package execution

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func BenchmarkObserveSessionEventModels(b *testing.B) {
	events := benchmarkModelEvents()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, event := range events {
			_ = ObserveSessionEventModels(event)
		}
	}
}

func BenchmarkSessionUsageCollectorModelEvents(b *testing.B) {
	events := benchmarkModelEvents()
	collector := NewSessionUsageCollector()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, event := range events {
			collector.On(event)
		}
	}
}

func benchmarkModelEvents() []copilot.SessionEvent {
	model := "gpt-5"
	return []copilot.SessionEvent{
		{Data: &copilot.SessionStartData{SelectedModel: &model}},
		{Data: &copilot.AssistantTurnStartData{Model: &model}},
		{Data: &copilot.ToolExecutionStartData{Model: &model, ToolName: "view", ToolCallID: "call-1"}},
		{Data: &copilot.ToolExecutionCompleteData{Model: &model, ToolCallID: "call-1", Success: true}},
		{Data: &copilot.AssistantUsageData{Model: model}},
		{Data: &copilot.AssistantMessageData{Model: &model, Content: "done"}},
	}
}
