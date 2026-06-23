package execution

import (
	"strings"
	"testing"
)

func TestParseStream_TextOnly(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}}`,
		`{"type":"result","result":"Hello world","session_id":"sess-1","total_cost_usd":0.001}`,
	}, "\n")

	result, err := parseStream(strings.NewReader(ndjson), nil)
	if err != nil {
		t.Fatalf("parseStream() error: %v", err)
	}
	if result.FinalOutput != "Hello world" {
		t.Errorf("FinalOutput = %q, want %q", result.FinalOutput, "Hello world")
	}
	if result.ClaudeSessionID != "sess-1" {
		t.Errorf("ClaudeSessionID = %q, want %q", result.ClaudeSessionID, "sess-1")
	}
	if !result.Success {
		t.Error("Success should be true when result event is present")
	}
	if result.TotalCostUSD != 0.001 {
		t.Errorf("TotalCostUSD = %f, want 0.001", result.TotalCostUSD)
	}
}

func TestParseStream_ToolCall(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-2"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"tu_1","name":"Bash"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"command\""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":":\"/bin/ls -la\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop"}}`,
		`{"type":"result","result":"done","session_id":"sess-2","total_cost_usd":0.002}`,
	}, "\n")

	result, err := parseStream(strings.NewReader(ndjson), nil)
	if err != nil {
		t.Fatalf("parseStream() error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.Name != "Bash" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "Bash")
	}
	if tc.Arguments.Command != "/bin/ls -la" {
		t.Errorf("ToolCall.Arguments.Command = %q, want %q", tc.Arguments.Command, "/bin/ls -la")
	}
	if !tc.Success {
		t.Error("ToolCall.Success should be true (CLI-side execution is assumed complete)")
	}
}

func TestParseStream_NoResultEvent(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-3"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial output"}}}`,
	}, "\n")

	result, err := parseStream(strings.NewReader(ndjson), nil)
	if err != nil {
		t.Fatalf("parseStream() error: %v", err)
	}
	if result.Success {
		t.Error("Success should be false when no result event is present")
	}
	// FinalOutput falls back to accumulated text when result event is absent.
	if result.FinalOutput != "partial output" {
		t.Errorf("FinalOutput = %q, want %q", result.FinalOutput, "partial output")
	}
}
