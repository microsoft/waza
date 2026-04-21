package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectEvents drains the iterator and returns all events and the first error (if any).
func collectEvents(t *testing.T, path string) ([]copilot.SessionEvent, error) {
	t.Helper()
	var events []copilot.SessionEvent

	for ev, err := range NewCopilotLogIterator(path) {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}

	return events, nil
}

func TestLogIterator_EmptyFile(t *testing.T) {
	events, err := collectEvents(t, filepath.Join("testdata", "empty.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestLogIterator_SingleValidLine(t *testing.T) {
	events, err := collectEvents(t, filepath.Join("testdata", "single_valid.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, copilot.SessionStart, events[0].Type)
}

func TestLogIterator_MultipleValidLines(t *testing.T) {
	events, err := collectEvents(t, filepath.Join("testdata", "multiple_valid.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, copilot.SessionStart, events[0].Type)
	assert.Equal(t, copilot.UserMessage, events[1].Type)
	assert.Equal(t, copilot.AssistantTurnStart, events[2].Type)
}

func TestLogIterator_FileNotFound(t *testing.T) {
	events, err := collectEvents(t, filepath.Join("testdata", "does_not_exist.jsonl"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))
	assert.Empty(t, events)
}

func TestLogIterator_MalformedJSON_MissingBrace(t *testing.T) {
	// The second line is missing its closing brace.
	// json.Decoder will parse the first valid object, then fail on the malformed one.
	events, err := collectEvents(t, filepath.Join("testdata", "malformed_missing_brace.jsonl"))
	require.Error(t, err)
	// First line was valid and should have been yielded before the error
	assert.Len(t, events, 1)
	assert.Equal(t, copilot.SessionStart, events[0].Type)
}

func TestLogIterator_MalformedJSON_InvalidSyntax(t *testing.T) {
	// Write a temp file with completely invalid JSON
	tmp := filepath.Join(t.TempDir(), "bad.jsonl")
	require.NoError(t, os.WriteFile(tmp, []byte("this is not json at all\n"), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	assert.Empty(t, events)
}

func TestLogIterator_TruncatedLog_EndsWithPartialJSON(t *testing.T) {
	// File ends mid-object — decoder should return an error
	tmp := filepath.Join(t.TempDir(), "truncated.jsonl")
	content := `{"type":"session.start","data":{"sessionId":"abc"},"id":"evt-1","timestamp":"2026-01-01T00:00:00Z","parentId":null}
{"type":"user.message","data":{"content":"hel`
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	// The first complete line should have been yielded
	assert.Len(t, events, 1)
}

func TestLogIterator_UnknownEventTypes(t *testing.T) {
	// The iterator should not reject unknown event types — it just decodes JSON.
	events, err := collectEvents(t, filepath.Join("testdata", "unknown_event_types.jsonl"))
	require.NoError(t, err)
	require.Len(t, events, 4)

	assert.Equal(t, copilot.SessionEventType("totally.made.up"), events[1].Type)
	assert.Equal(t, copilot.SessionEventType("🦄.unicorn"), events[2].Type)
	assert.Equal(t, copilot.AssistantTurnEnd, events[3].Type)
}

func TestLogIterator_VeryLargeLogLine(t *testing.T) {
	// Build a single valid JSON event with a very large content field (~1 MB)
	bigContent := strings.Repeat("A", 1<<20) // 1 MiB
	tmp := filepath.Join(t.TempDir(), "large.jsonl")
	line := `{"type":"user.message","data":{"content":"` + bigContent + `"},"id":"evt-1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(line), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, copilot.UserMessage, events[0].Type)
	require.NotNil(t, events[0].Data.Content)
	assert.Len(t, *events[0].Data.Content, 1<<20)
}

func TestLogIterator_BinaryNullBytes(t *testing.T) {
	// A JSON line with embedded null bytes in the content value.
	// json.Decoder can handle \u0000 in strings, but raw NUL bytes outside strings
	// are not valid JSON and should cause an error.
	tmp := filepath.Join(t.TempDir(), "binary.jsonl")
	// Raw NUL byte between two valid JSON objects — decoder should fail on the garbage.
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n" +
		"\x00\x00\x00\n" +
		`{"type":"assistant.turn_end","data":{},"id":"e2","timestamp":"2026-01-01T00:00:01Z","parentId":"e1"}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	// First line should succeed
	assert.Len(t, events, 1)
}

func TestLogIterator_EscapedNullInJSON(t *testing.T) {
	// A properly escaped null character (\u0000) inside a JSON string is valid.
	tmp := filepath.Join(t.TempDir(), "escaped_null.jsonl")
	content := `{"type":"user.message","data":{"content":"before\u0000after"},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Data.Content)
	assert.Contains(t, *events[0].Data.Content, "\x00")
}

func TestLogIterator_EmptyJSONObjects(t *testing.T) {
	// Minimal valid JSON that can decode into SessionEvent (missing fields get zero values)
	tmp := filepath.Join(t.TempDir(), "empty_objs.jsonl")
	content := "{}\n{}\n{}\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	assert.Len(t, events, 3)
	for _, ev := range events {
		assert.Equal(t, copilot.SessionEventType(""), ev.Type)
	}
}

func TestLogIterator_WhitespaceOnlyFile(t *testing.T) {
	// A file with just whitespace — json.Decoder should reach EOF without errors
	tmp := filepath.Join(t.TempDir(), "whitespace.jsonl")
	require.NoError(t, os.WriteFile(tmp, []byte("   \n\n  \t\n"), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestLogIterator_ExtraWhitespaceBetweenObjects(t *testing.T) {
	// json.Decoder handles arbitrary whitespace between top-level JSON values
	tmp := filepath.Join(t.TempDir(), "extra_ws.jsonl")
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}


	   {"type":"assistant.turn_end","data":{},"id":"e2","timestamp":"2026-01-01T00:00:01Z","parentId":"e1"}

`
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestLogIterator_EarlyStopViaBreak(t *testing.T) {
	// Verify that breaking out of the range loop early (consumer stopping) works correctly.
	events, err := collectEvents(t, filepath.Join("testdata", "sample_events.jsonl"))
	require.NoError(t, err)
	require.Greater(t, len(events), 2, "sample_events should have multiple entries")

	// Now iterate but stop after 1 event
	var count int
	for _, err := range NewCopilotLogIterator(filepath.Join("testdata", "sample_events.jsonl")) {
		require.NoError(t, err)
		count++
		if count >= 1 {
			break
		}
	}
	assert.Equal(t, 1, count)
}

func TestLogIterator_TrailingCommaInJSON(t *testing.T) {
	// Trailing commas are invalid JSON — decoder should error
	tmp := filepath.Join(t.TempDir(), "trailing_comma.jsonl")
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null,}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	assert.Empty(t, events)
}

func TestLogIterator_JSONArray(t *testing.T) {
	// A JSON array is not valid JSONL — decoder will try to decode the array
	// as a SessionEvent and fail since arrays can't unmarshal into a struct.
	tmp := filepath.Join(t.TempDir(), "array.jsonl")
	content := `[{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}]` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	assert.Empty(t, events)
}

func TestLogIterator_MixedValidAndBareStrings(t *testing.T) {
	// A bare string token (e.g., "hello") is valid JSON but can't decode into SessionEvent
	tmp := filepath.Join(t.TempDir(), "bare_string.jsonl")
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n" +
		`"just a string"` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.Error(t, err)
	// First valid event should have been yielded
	assert.Len(t, events, 1)
}

func TestLogIterator_NullLiteral(t *testing.T) {
	// json null decodes to a nil pointer; the iterator dereferences *event,
	// so this tests the null-to-zero-value path.
	tmp := filepath.Join(t.TempDir(), "null_literal.jsonl")
	content := "null\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	// The iterator does `var event *copilot.SessionEvent` then `Decode(&event)`.
	// A JSON `null` sets event to nil. Dereferencing nil => panic.
	// This documents the current behavior: null lines cause a panic.
	assert.Panics(t, func() {
		for _, err := range NewCopilotLogIterator(tmp) {
			_ = err
		}
	})
}

func TestLogIterator_ExtraFieldsIgnored(t *testing.T) {
	// Extra fields not in the struct should be silently ignored by json.Decoder
	tmp := filepath.Join(t.TempDir(), "extra_fields.jsonl")
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null,"extraField":"should be ignored","nested":{"a":1}}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, copilot.SessionStart, events[0].Type)
}

func TestLogIterator_InvalidUTF8InContent(t *testing.T) {
	// Invalid UTF-8 bytes inside a JSON string value.
	// encoding/json is lenient with raw bytes in strings.
	tmp := filepath.Join(t.TempDir(), "bad_utf8.jsonl")
	content := `{"type":"user.message","data":{"content":"valid` + "\xff\xfe" + `still"},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	// Go's json decoder accepts raw bytes in strings; it doesn't enforce strict UTF-8
	require.NoError(t, err)
	require.Len(t, events, 1)
}

func TestLogIterator_ConcatenatedJSON_NoNewlines(t *testing.T) {
	// json.Decoder handles concatenated JSON objects without newlines between them
	tmp := filepath.Join(t.TempDir(), "concat.jsonl")
	content := `{"type":"session.start","data":{},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}{"type":"assistant.turn_end","data":{},"id":"e2","timestamp":"2026-01-01T00:00:01Z","parentId":"e1"}`
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestLogIterator_DeeplyNestedJSON(t *testing.T) {
	// Events with deeply nested data should decode fine
	tmp := filepath.Join(t.TempDir(), "nested.jsonl")
	content := `{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"echo","nested":{"deep":{"deeper":{"deepest":"value"}}}}},"id":"e1","timestamp":"2026-01-01T00:00:00Z","parentId":null}` + "\n"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))

	events, err := collectEvents(t, tmp)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, copilot.ToolExecutionStart, events[0].Type)
}
