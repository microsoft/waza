package webapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunEventsFromDetailGeneratesSequencedLifecycle(t *testing.T) {
	detail := sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Date(2026, 6, 30, 13, 0, 0, 0, time.UTC))

	events := runEventsFromDetail(detail)
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}

	wantTypes := []RunEventType{
		RunEventStarted,
		RunEventTaskStarted,
		RunEventStepExecuted,
		RunEventTaskCompleted,
		RunEventCompleted,
	}
	for i, want := range wantTypes {
		if events[i].Sequence != int64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, events[i].Sequence, i+1)
		}
		if events[i].Type != want {
			t.Fatalf("event %d type = %q, want %q", i, events[i].Type, want)
		}
		if events[i].RunID != "run-001" {
			t.Fatalf("event %d run id = %q", i, events[i].RunID)
		}
	}

	if events[0].Data.TotalTasks != 1 {
		t.Fatalf("run_started totalTasks = %d, want 1", events[0].Data.TotalTasks)
	}
	if events[2].Data.GraderName != "code_validator" {
		t.Fatalf("step_executed graderName = %q", events[2].Data.GraderName)
	}
}

func TestRunEventsFromDetailMarksFailedRun(t *testing.T) {
	detail := sampleRun("run-002", "code-explainer", "gpt-4o", 0, 1, 1000, time.Now())

	events := runEventsFromDetail(detail)
	last := events[len(events)-1]
	if last.Type != RunEventFailed {
		t.Fatalf("last event type = %q, want %q", last.Type, RunEventFailed)
	}
	if last.Data.FailCount != 1 {
		t.Fatalf("fail count = %d, want 1", last.Data.FailCount)
	}
}

func TestRunEventsFromDetailOmitsTerminalEventForRunningRun(t *testing.T) {
	detail := sampleRun("run-live", "code-explainer", "gpt-4o", 1, 2, 1000, time.Now())
	detail.Outcome = "running"

	events := runEventsFromDetail(detail)
	if len(events) == 0 {
		t.Fatal("expected non-terminal events")
	}
	last := events[len(events)-1]
	if last.Type == RunEventCompleted || last.Type == RunEventFailed {
		t.Fatalf("last event type = %q, want non-terminal", last.Type)
	}
}

func TestHandleRunEventsStreamsSSE(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Now()))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-001/events", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) != 5 {
		t.Fatalf("expected 5 SSE events, got %d", len(events))
	}
	if events[0].Type != RunEventStarted {
		t.Fatalf("first event = %q, want %q", events[0].Type, RunEventStarted)
	}
	if events[len(events)-1].Type != RunEventCompleted {
		t.Fatalf("last event = %q, want %q", events[len(events)-1].Type, RunEventCompleted)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "retry: 1000\n\n") {
		t.Fatal("expected SSE retry directive")
	}
	if !strings.Contains(body, "id: 1\n") {
		t.Fatal("expected event-stream id framing")
	}
}

func TestHandleRunEventsReconnectUsesLastEventID(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Now()))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-001/events", nil)
	req.Header.Set("Last-Event-ID", "3")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) != 2 {
		t.Fatalf("expected 2 recovered events, got %d", len(events))
	}
	if events[0].Sequence != 4 {
		t.Fatalf("first recovered sequence = %d, want 4", events[0].Sequence)
	}
}

func TestHandleRunEventsReconnectUsesLastEventIDQueryParam(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Now()))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-001/events?lastEventId=3", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) != 2 {
		t.Fatalf("expected 2 recovered events, got %d", len(events))
	}
	if events[0].Sequence != 4 {
		t.Fatalf("first recovered sequence = %d, want 4", events[0].Sequence)
	}
}

func TestHandleRunEventsInvalidLastEventID(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Now()))
	h := NewHandlers(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-001/events", nil)
	req.Header.Set("Last-Event-ID", "bad")
	rec := httptest.NewRecorder()
	h.HandleRunEvents(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleRunEventsRejectsMalformedFallbackPath(t *testing.T) {
	h := NewHandlers(newMockStore())

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	h.HandleRunEvents(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRunIDFromEventsPathRequiresV1RunEventsShape(t *testing.T) {
	tests := map[string]string{
		"/api/v1/runs/run-001/events": "run-001",
		"/api/v1/runs//events":        "",
		"/api/events":                 "",
		"/api/v1/runs/run-001":        "",
		"/api/v1/runs/a/b/events":     "",
	}

	for path, want := range tests {
		if got := runIDFromEventsPath(path); got != want {
			t.Fatalf("runIDFromEventsPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestHandleRunEventsStreamsNewEventsUntilTerminal(t *testing.T) {
	oldInterval := runEventsPollInterval
	runEventsPollInterval = 10 * time.Millisecond
	defer func() { runEventsPollInterval = oldInterval }()

	first := sampleRun("run-live", "code-explainer", "gpt-4o", 1, 2, 1000, time.Now())
	first.Outcome = "running"
	final := sampleRun("run-live", "code-explainer", "gpt-4o", 2, 2, 1000, first.Timestamp)
	final.Tasks = append(final.Tasks, TaskResult{
		Name:     "explain-factorial",
		Outcome:  "passed",
		Score:    1,
		Duration: 21,
		GraderResults: []GraderResult{
			{
				Name:    "code_validator",
				Type:    "code",
				Passed:  true,
				Score:   1,
				Message: "All assertions passed",
			},
		},
	})

	store := newLiveRunStore(first)
	mux := http.NewServeMux()
	RegisterRoutes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		store.setRun(final)
	}()

	resp, err := http.Get(ts.URL + "/api/v1/runs/run-live/events") //nolint:gosec,noctx
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	events := readSSEEventsUntil(t, resp.Body, RunEventCompleted)
	if len(events) != 8 {
		t.Fatalf("expected 8 streamed events, got %d", len(events))
	}
	if events[4].Sequence != 5 || events[4].Data.TaskName != "explain-factorial" {
		t.Fatalf("first live delta = sequence %d task %q, want sequence 5 explain-factorial", events[4].Sequence, events[4].Data.TaskName)
	}
	if events[len(events)-1].Type != RunEventCompleted {
		t.Fatalf("last event = %q, want %q", events[len(events)-1].Type, RunEventCompleted)
	}
}

func TestHandleLatestRunEventsUsesNewestRun(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("old", "old-spec", "gpt-4o", 1, 1, 1000, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	store.addRun(sampleRun("new", "new-spec", "gpt-4o", 1, 1, 1000, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	events := decodeSSEEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	if events[0].RunID != "new" {
		t.Fatalf("legacy stream run id = %q, want new", events[0].RunID)
	}
}

func TestHandleRunEventsConcurrentClients(t *testing.T) {
	store := newMockStore()
	store.addRun(sampleRun("run-001", "code-explainer", "gpt-4o", 1, 1, 1000, time.Now()))

	mux := http.NewServeMux()
	RegisterRoutes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	const clients = 25
	var wg sync.WaitGroup
	errCh := make(chan error, clients)
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(ts.URL + "/api/v1/runs/run-001/events") //nolint:gosec,noctx
			if err != nil {
				errCh <- err
				return
			}
			defer resp.Body.Close() //nolint:errcheck
			if resp.StatusCode != http.StatusOK {
				errCh <- &statusError{status: resp.StatusCode}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type liveRunStore struct {
	mu     sync.RWMutex
	detail *RunDetail
}

func newLiveRunStore(detail *RunDetail) *liveRunStore {
	return &liveRunStore{detail: detail}
}

func (s *liveRunStore) setRun(detail *RunDetail) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detail = detail
}

func (s *liveRunStore) ListRuns(_, _ string) ([]RunSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return []RunSummary{s.detail.RunSummary}, nil
}

func (s *liveRunStore) GetRun(id string) (*RunDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.detail == nil || s.detail.ID != id {
		return nil, ErrRunNotFound
	}
	return s.detail, nil
}

func (s *liveRunStore) Summary() (*SummaryResponse, error) {
	return &SummaryResponse{}, nil
}

type statusError struct {
	status int
}

func (e *statusError) Error() string {
	return "unexpected status " + http.StatusText(e.status)
}

func decodeSSEEvents(t *testing.T, body string) []RunEvent {
	t.Helper()

	var events []RunEvent
	var dataLines []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		var event RunEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &event); err != nil {
			t.Fatalf("decode SSE data: %v", err)
		}
		events = append(events, event)
		dataLines = nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	flush()
	return events
}

func readSSEEventsUntil(t *testing.T, r io.Reader, want RunEventType) []RunEvent {
	t.Helper()

	var events []RunEvent
	var dataLines []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
			continue
		}
		if len(dataLines) == 0 {
			continue
		}
		var event RunEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &event); err != nil {
			t.Fatalf("decode SSE data: %v", err)
		}
		events = append(events, event)
		dataLines = nil
		if event.Type == want {
			return events
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	t.Fatalf("stream ended before %q event", want)
	return nil
}
