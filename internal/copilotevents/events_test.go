package copilotevents

import (
	"encoding/json"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func evt(data copilot.SessionEventData) copilot.SessionEvent {
	return copilot.SessionEvent{Data: data}
}

func TestContent(t *testing.T) {
	cases := []struct {
		name string
		evt  copilot.SessionEvent
		want string
		ok   bool
	}{
		{"user", evt(&copilot.UserMessageData{Content: "hello"}), "hello", true},
		{"assistant", evt(&copilot.AssistantMessageData{Content: "hi"}), "hi", true},
		{"reasoning", evt(&copilot.AssistantReasoningData{Content: "thinking"}), "thinking", true},
		{"system", evt(&copilot.SystemMessageData{Content: "sys"}), "sys", true},
		{"other", evt(&copilot.SessionStartData{}), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Content(c.evt)
			if got != c.want || ok != c.ok {
				t.Fatalf("Content() = (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestDeltaContent(t *testing.T) {
	if got, ok := DeltaContent(evt(&copilot.AssistantMessageDeltaData{DeltaContent: "delta"})); !ok || got != "delta" {
		t.Fatalf("DeltaContent = (%q, %v)", got, ok)
	}
	if got, ok := DeltaContent(evt(&copilot.UserMessageData{})); ok || got != "" {
		t.Fatalf("DeltaContent on non-delta returned (%q, %v)", got, ok)
	}
}

func TestMessage(t *testing.T) {
	cases := []struct {
		name string
		evt  copilot.SessionEvent
		want string
		ok   bool
	}{
		{"error", evt(&copilot.SessionErrorData{Message: "boom"}), "boom", true},
		{"info", evt(&copilot.SessionInfoData{Message: "fyi"}), "fyi", true},
		{"warning", evt(&copilot.SessionWarningData{Message: "warn"}), "warn", true},
		{"other", evt(&copilot.UserMessageData{}), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Message(c.evt)
			if got != c.want || ok != c.ok {
				t.Fatalf("Message = (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestReasoningText(t *testing.T) {
	text := "because"
	if got := ReasoningText(evt(&copilot.AssistantMessageData{ReasoningText: &text})); got == nil || *got != text {
		t.Fatalf("ReasoningText = %v", got)
	}
	if got := ReasoningText(evt(&copilot.UserMessageData{})); got != nil {
		t.Fatalf("ReasoningText on non-assistant got %v", got)
	}
}

func TestTypedAccessors(t *testing.T) {
	if _, ok := SessionStart(evt(&copilot.SessionStartData{})); !ok {
		t.Fatal("SessionStart")
	}
	if _, ok := SessionStart(evt(&copilot.UserMessageData{})); ok {
		t.Fatal("SessionStart should miss")
	}
	if _, ok := ToolStart(evt(&copilot.ToolExecutionStartData{})); !ok {
		t.Fatal("ToolStart")
	}
	if _, ok := ToolUserRequested(evt(&copilot.ToolUserRequestedData{})); !ok {
		t.Fatal("ToolUserRequested")
	}
	if _, ok := ToolComplete(evt(&copilot.ToolExecutionCompleteData{})); !ok {
		t.Fatal("ToolComplete")
	}
	if _, ok := ToolPartial(evt(&copilot.ToolExecutionPartialResultData{})); !ok {
		t.Fatal("ToolPartial")
	}
	if _, ok := ToolProgress(evt(&copilot.ToolExecutionProgressData{})); !ok {
		t.Fatal("ToolProgress")
	}
	if _, ok := SkillInvoked(evt(&copilot.SkillInvokedData{})); !ok {
		t.Fatal("SkillInvoked")
	}
	if _, ok := HookStart(evt(&copilot.HookStartData{})); !ok {
		t.Fatal("HookStart")
	}
	if _, ok := HookEnd(evt(&copilot.HookEndData{})); !ok {
		t.Fatal("HookEnd")
	}
	if _, ok := Shutdown(evt(&copilot.SessionShutdownData{})); !ok {
		t.Fatal("Shutdown")
	}
	if _, ok := AssistantUsage(evt(&copilot.AssistantUsageData{})); !ok {
		t.Fatal("AssistantUsage")
	}
}

func TestToolCallID(t *testing.T) {
	cases := []struct {
		name string
		evt  copilot.SessionEvent
		want string
		ok   bool
	}{
		{"start", evt(&copilot.ToolExecutionStartData{ToolCallID: "a"}), "a", true},
		{"complete", evt(&copilot.ToolExecutionCompleteData{ToolCallID: "b"}), "b", true},
		{"partial", evt(&copilot.ToolExecutionPartialResultData{ToolCallID: "c"}), "c", true},
		{"progress", evt(&copilot.ToolExecutionProgressData{ToolCallID: "d"}), "d", true},
		{"requested", evt(&copilot.ToolUserRequestedData{ToolCallID: "e"}), "e", true},
		{"empty", evt(&copilot.ToolExecutionStartData{}), "", false},
		{"other", evt(&copilot.UserMessageData{}), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ToolCallID(c.evt)
			if got != c.want || ok != c.ok {
				t.Fatalf("ToolCallID = (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestRawData(t *testing.T) {
	payload := map[string]any{"foo": "bar"}
	got := RawData(copilot.SessionEventTypeSessionInfo, payload)
	raw, ok := got.(*copilot.RawSessionEventData)
	if !ok {
		t.Fatalf("RawData returned %T", got)
	}
	if raw.EventType != copilot.SessionEventTypeSessionInfo {
		t.Fatalf("EventType = %v", raw.EventType)
	}
	var back map[string]any
	if err := json.Unmarshal(raw.Raw, &back); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if back["foo"] != "bar" {
		t.Fatalf("Raw = %s", raw.Raw)
	}

	// unmarshalable payload falls back to "{}"
	bad := RawData(copilot.SessionEventTypeSessionInfo, make(chan int))
	rd, ok := bad.(*copilot.RawSessionEventData)
	if !ok {
		t.Fatalf("expected *RawSessionEventData, got %T", bad)
	}
	if string(rd.Raw) != "{}" {
		t.Fatalf("expected fallback {}, got %s", rd.Raw)
	}
}
