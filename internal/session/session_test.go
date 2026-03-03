package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	data := map[string]any{"key": "value"}
	ev := NewEvent(EventSessionStart, data)

	if ev.Type != EventSessionStart {
		t.Errorf("Type = %q, want %q", ev.Type, EventSessionStart)
	}
	if ev.Data["key"] != "value" {
		t.Errorf("Data[key] = %v, want %q", ev.Data["key"], "value")
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestEventJSON(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	ev := Event{
		Timestamp: ts,
		Type:      EventTaskStart,
		Data:      TaskStartData("my-task", 1, 3),
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Type != EventTaskStart {
		t.Errorf("decoded.Type = %q, want %q", decoded.Type, EventTaskStart)
	}
	if !decoded.Timestamp.Equal(ts) {
		t.Errorf("decoded.Timestamp = %v, want %v", decoded.Timestamp, ts)
	}
	if decoded.Data["task_name"] != "my-task" {
		t.Errorf("task_name = %v, want %q", decoded.Data["task_name"], "my-task")
	}
}

func TestSessionStartData(t *testing.T) {
	d := SessionStartData("/path/eval.yaml", "gpt-4o", "copilot-sdk", 5)
	if d["spec_path"] != "/path/eval.yaml" {
		t.Errorf("spec_path = %v", d["spec_path"])
	}
	if d["task_count"] != 5 {
		t.Errorf("task_count = %v", d["task_count"])
	}
}

func TestGraderResultData(t *testing.T) {
	d := GraderResultData("text-check", "text", true, 1.0, "all patterns matched")
	if d["grader_name"] != "text-check" {
		t.Errorf("grader_name = %v", d["grader_name"])
	}
	if d["passed"] != true {
		t.Errorf("passed = %v", d["passed"])
	}
}

func TestErrorData(t *testing.T) {
	d := ErrorData("timeout exceeded", map[string]any{"task": "foo"})
	if d["message"] != "timeout exceeded" {
		t.Errorf("message = %v", d["message"])
	}
	if d["task"] != "foo" {
		t.Errorf("task = %v", d["task"])
	}
}

func TestJSONLogger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")

	logger, err := NewJSONLogger(path)
	if err != nil {
		t.Fatalf("NewJSONLogger: %v", err)
	}

	events := []Event{
		NewEvent(EventSessionStart, SessionStartData("eval.yaml", "gpt-4o", "mock", 2)),
		NewEvent(EventTaskStart, TaskStartData("task-1", 1, 2)),
		NewEvent(EventTaskComplete, TaskCompleteData("task-1", "passed", 1.0, 500)),
		NewEvent(EventSessionEnd, SessionCompleteData(2, 2, 0, 0, 1000)),
	}

	for _, ev := range events {
		if err := logger.Log(ev); err != nil {
			t.Fatalf("Log: %v", err)
		}
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the file was written with one JSON object per line
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}

	// Parse first line
	var first Event
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("Unmarshal line 0: %v", err)
	}
	if first.Type != EventSessionStart {
		t.Errorf("first event type = %q, want %q", first.Type, EventSessionStart)
	}
}

func TestJSONLoggerPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.jsonl")

	logger, err := NewJSONLogger(path)
	if err != nil {
		t.Fatalf("NewJSONLogger with subdirectory: %v", err)
	}
	defer logger.Close() //nolint:errcheck

	if logger.Path() != path {
		t.Errorf("Path() = %q, want %q", logger.Path(), path)
	}
}

func TestNopLogger(t *testing.T) {
	var logger Logger = NopLogger{}
	if err := logger.Log(NewEvent(EventSessionStart, nil)); err != nil {
		t.Errorf("NopLogger.Log should not error: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Errorf("NopLogger.Close should not error: %v", err)
	}
}

func TestDefaultLogPath(t *testing.T) {
	p := DefaultLogPath("/tmp/sessions")
	if filepath.Dir(p) != filepath.FromSlash("/tmp/sessions") {
		t.Errorf("dir = %q, want %s", filepath.Dir(p), filepath.FromSlash("/tmp/sessions"))
	}
	if ext := filepath.Ext(p); ext != ".jsonl" {
		t.Errorf("ext = %q, want .jsonl", ext)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()

	// Create some session files
	for _, name := range []string{
		"20250115T100000Z-session.jsonl",
		"20250116T100000Z-session.jsonl",
		"not-a-session.txt",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0644) //nolint:errcheck
	}

	files, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
}

func TestListSessionsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	files, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("got %d files, want 0", len(files))
	}
}

func TestListSessionsNoDir(t *testing.T) {
	_, err := ListSessions("/nonexistent/dir")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestReadEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")

	// Write NDJSON
	logger, err := NewJSONLogger(path)
	if err != nil {
		t.Fatalf("NewJSONLogger: %v", err)
	}
	logger.Log(NewEvent(EventSessionStart, SessionStartData("e.yaml", "m", "mock", 1))) //nolint:errcheck
	logger.Log(NewEvent(EventTaskStart, TaskStartData("t1", 1, 1)))                     //nolint:errcheck
	logger.Log(NewEvent(EventTaskComplete, TaskCompleteData("t1", "passed", 1.0, 100))) //nolint:errcheck
	logger.Log(NewEvent(EventSessionEnd, SessionCompleteData(1, 1, 0, 0, 100)))         //nolint:errcheck
	logger.Close()                                                                      //nolint:errcheck

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	if events[0].Type != EventSessionStart {
		t.Errorf("events[0].Type = %q", events[0].Type)
	}
	if events[3].Type != EventSessionEnd {
		t.Errorf("events[3].Type = %q", events[3].Type)
	}
}

func TestReadEventsSkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session.jsonl")

	content := `{"timestamp":"2025-01-15T10:00:00Z","type":"session_start","data":{}}
not valid json
{"timestamp":"2025-01-15T10:00:01Z","type":"session_complete","data":{}}
`
	os.WriteFile(path, []byte(content), 0644) //nolint:errcheck

	events, err := ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (malformed line skipped)", len(events))
	}
}

func TestRenderTimeline(t *testing.T) {
	base := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Timestamp: base, Type: EventSessionStart, Data: SessionStartData("e.yaml", "gpt-4o", "mock", 2)},
		{Timestamp: base.Add(100 * time.Millisecond), Type: EventTaskStart, Data: TaskStartData("task-1", 1, 2)},
		{Timestamp: base.Add(200 * time.Millisecond), Type: EventGraderResult, Data: GraderResultData("text", "text", true, 1.0, "ok")},
		{Timestamp: base.Add(300 * time.Millisecond), Type: EventTaskComplete, Data: TaskCompleteData("task-1", "passed", 1.0, 200)},
		{Timestamp: base.Add(400 * time.Millisecond), Type: EventError, Data: ErrorData("something broke", nil)},
		{Timestamp: base.Add(500 * time.Millisecond), Type: EventSessionEnd, Data: SessionCompleteData(2, 1, 1, 0, 500)},
	}

	var buf bytes.Buffer
	RenderTimeline(&buf, events)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("SESSION TIMELINE")) {
		t.Error("output should contain SESSION TIMELINE header")
	}
	if !bytes.Contains([]byte(output), []byte("task-1")) {
		t.Error("output should contain task name")
	}
	if !bytes.Contains([]byte(output), []byte("gpt-4o")) {
		t.Error("output should contain model name")
	}
	if !bytes.Contains([]byte(output), []byte("something broke")) {
		t.Error("output should contain error message")
	}
}

func TestRenderTimelineEmpty(t *testing.T) {
	var buf bytes.Buffer
	RenderTimeline(&buf, nil)
	if !bytes.Contains(buf.Bytes(), []byte("No events found.")) {
		t.Error("empty events should print 'No events found.'")
	}
}
