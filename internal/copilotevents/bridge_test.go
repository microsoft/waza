package copilotevents

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/microsoft/waza/internal/agentevent"
)

func TestFromSDKEmpty(t *testing.T) {
	if got := FromSDK(nil); got != nil {
		t.Fatalf("FromSDK(nil) = %v", got)
	}
}

func TestFromSDKWrapsAndKinds(t *testing.T) {
	sdkEvents := []copilot.SessionEvent{
		{Data: &copilot.SessionStartData{}},
		{Data: &copilot.SessionShutdownData{}},
		{Data: &copilot.SessionErrorData{Message: "err"}},
		{Data: &copilot.SessionInfoData{Message: "info"}},
		{Data: &copilot.SessionWarningData{Message: "warn"}},
		{Data: &copilot.UserMessageData{Content: "u"}},
		{Data: &copilot.AssistantMessageData{Content: "a"}},
		{Data: &copilot.AssistantMessageDeltaData{DeltaContent: "d"}},
		{Data: &copilot.AssistantReasoningData{Content: "r"}},
		{Data: &copilot.AssistantUsageData{}},
		{Data: &copilot.SystemMessageData{Content: "s"}},
		{Data: &copilot.ToolExecutionStartData{ToolCallID: "1"}},
		{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "1"}},
		{Data: &copilot.ToolExecutionPartialResultData{ToolCallID: "1"}},
		{Data: &copilot.ToolExecutionProgressData{ToolCallID: "1"}},
		{Data: &copilot.ToolUserRequestedData{ToolCallID: "1"}},
		{Data: &copilot.SkillInvokedData{}},
		{Data: &copilot.HookStartData{}},
		{Data: &copilot.HookEndData{}},
		// Unknown SDK event type falls through to KindRaw via RawSessionEventData.
		{Data: &copilot.RawSessionEventData{EventType: copilot.SessionEventType("custom.unknown")}},
	}
	expectedKinds := []agentevent.Kind{
		agentevent.KindSessionStart,
		agentevent.KindSessionShutdown,
		agentevent.KindSessionError,
		agentevent.KindSessionInfo,
		agentevent.KindSessionWarning,
		agentevent.KindUserMessage,
		agentevent.KindAssistantMessage,
		agentevent.KindAssistantMessageDelta,
		agentevent.KindAssistantReasoning,
		agentevent.KindAssistantUsage,
		agentevent.KindSystemMessage,
		agentevent.KindToolExecutionStart,
		agentevent.KindToolExecutionComplete,
		agentevent.KindToolExecutionPartialResult,
		agentevent.KindToolExecutionProgress,
		agentevent.KindToolUserRequested,
		agentevent.KindSkillInvoked,
		agentevent.KindHookStart,
		agentevent.KindHookEnd,
		agentevent.KindRaw,
	}

	out := FromSDK(sdkEvents)
	if len(out) != len(sdkEvents) {
		t.Fatalf("FromSDK len = %d, want %d", len(out), len(sdkEvents))
	}
	for i, e := range out {
		if e.Kind() != expectedKinds[i] {
			t.Errorf("event %d kind = %s, want %s", i, e.Kind(), expectedKinds[i])
		}
		// Raw round-trip preserves the SDK event.
		se, ok := AsSDKEvent(e)
		if !ok {
			t.Errorf("event %d: AsSDKEvent !ok", i)
			continue
		}
		if se.Type() != sdkEvents[i].Type() {
			t.Errorf("event %d type = %s, want %s", i, se.Type(), sdkEvents[i].Type())
		}
	}

	// ToSDK strips wrappers and preserves order.
	roundTrip := ToSDK(out)
	if len(roundTrip) != len(sdkEvents) {
		t.Fatalf("ToSDK len = %d, want %d", len(roundTrip), len(sdkEvents))
	}
}

func TestToSDKEmpty(t *testing.T) {
	if got := ToSDK(nil); got != nil {
		t.Fatalf("ToSDK(nil) = %v", got)
	}
}

func TestToSDKSkipsNonSDK(t *testing.T) {
	// agentevent.New with a non-SDK raw value should be skipped by ToSDK.
	nonSDK := agentevent.New(agentevent.KindRaw, "not-an-sdk-event")
	wrapped := WrapSDKEvent(copilot.SessionEvent{Data: &copilot.UserMessageData{Content: "x"}})
	out := ToSDK([]agentevent.Event{nonSDK, wrapped})
	if len(out) != 1 {
		t.Fatalf("ToSDK = %d events, want 1", len(out))
	}
}

func TestAsSDKEventMiss(t *testing.T) {
	e := agentevent.New(agentevent.KindRaw, 42)
	if _, ok := AsSDKEvent(e); ok {
		t.Fatal("AsSDKEvent should be false for non-SDK raw")
	}
}
