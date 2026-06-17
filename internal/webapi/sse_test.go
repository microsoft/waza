// Copyright (c) Microsoft Corporation. All rights reserved.

package webapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerPublishFanOut(t *testing.T) {
	t.Parallel()
	b := NewBroker()

	a, unsubA := b.Subscribe()
	defer unsubA()
	c, unsubC := b.Subscribe()
	defer unsubC()

	if got := b.SubscriberCount(); got != 2 {
		t.Fatalf("subscriber count = %d, want 2", got)
	}

	b.Publish(SSEEvent{Type: "task_start", Data: map[string]any{"taskName": "t1"}})

	for i, ch := range []<-chan SSEEvent{a, c} {
		select {
		case ev := <-ch:
			if ev.Type != "task_start" {
				t.Errorf("subscriber %d got type %q", i, ev.Type)
			}
			if ev.Timestamp.IsZero() {
				t.Errorf("subscriber %d: timestamp not set", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestBrokerDropsUnknownEventTypes(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	ch, unsub := b.Subscribe()
	defer unsub()

	b.Publish(SSEEvent{Type: "session_start"})
	b.Publish(SSEEvent{Type: "task_start", Data: map[string]any{"taskName": "t"}})

	select {
	case ev := <-ch:
		if ev.Type != "task_start" {
			t.Fatalf("expected task_start, got %q", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("expected task_start event")
	}

	select {
	case ev := <-ch:
		t.Fatalf("unexpected extra event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBrokerUnsubscribeRemovesSubscriber(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	_, unsub := b.Subscribe()
	if got := b.SubscriberCount(); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	unsub()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("count after unsubscribe = %d, want 0", got)
	}
	// Calling unsubscribe twice must be safe.
	unsub()
}

func TestHandleEventsServesEventStream(t *testing.T) {
	t.Parallel()
	h := NewHandlers(nil)
	h.SetBroker(NewBroker())

	mux := http.NewServeMux()
	RegisterRoutesWithHandlers(mux, h)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	// Publish from another goroutine.
	go func() {
		// Tiny delay so the SSE handler subscribes before we publish.
		time.Sleep(50 * time.Millisecond)
		h.PublishLiveEvent(SSEEvent{
			Type: "task_start",
			Data: map[string]any{"taskName": "alpha"},
		})
	}()

	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	var collected string
	for time.Now().Before(deadline) {
		_ = resp.Request.Context()
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if strings.Contains(collected, `"type":"task_start"`) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(collected, `"type":"task_start"`) {
		t.Fatalf("did not receive task_start event; got: %q", collected)
	}
	if !strings.Contains(collected, `"taskName":"alpha"`) {
		t.Errorf("expected taskName in payload; got: %q", collected)
	}
	if !strings.HasPrefix(collected, ": connected") {
		t.Errorf("expected initial \": connected\" comment; got: %q", collected)
	}
}

func TestHandleEventsReturns503WithoutBroker(t *testing.T) {
	t.Parallel()
	h := NewHandlers(nil)
	mux := http.NewServeMux()
	RegisterRoutesWithHandlers(mux, h)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestRegisterRoutesIncludesEvents(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	RegisterRoutes(mux, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	mux.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Fatalf("/api/events not registered (404)")
	}
}

func TestSessionLogTailerPublishesEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	broker := NewBroker()
	ch, unsub := broker.Subscribe()
	defer unsub()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer := NewSessionLogTailer(dir, broker, nil, 20*time.Millisecond)
	go tailer.Run(ctx)

	// Give the tailer a moment to record its initial-scan baseline.
	time.Sleep(50 * time.Millisecond)

	logPath := filepath.Join(dir, "20260101T000000Z-session.jsonl")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck

	writeEvent := func(t *testing.T, kind string, data map[string]any) {
		t.Helper()
		ev := map[string]any{
			"timestamp": time.Now().UTC(),
			"type":      kind,
			"data":      data,
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	writeEvent(t, "task_start", map[string]any{"task_name": "alpha", "task_num": 1, "total_tasks": 2})
	writeEvent(t, "task_complete", map[string]any{"task_name": "alpha", "status": "pass", "score": 1.0, "duration_ms": int64(123)})
	writeEvent(t, "grader_result", map[string]any{"grader_name": "code", "grader_type": "code", "passed": true, "score": 1.0, "feedback": "ok"})
	writeEvent(t, "session_complete", map[string]any{"total_tests": 1, "passed": 1, "failed": 0, "errors": 0, "duration_ms": int64(456)})

	want := []string{"task_start", "task_complete", "grader_result", "run_complete"}
	got := make([]string, 0, len(want))

	timeout := time.After(3 * time.Second)
	for len(got) < len(want) {
		select {
		case ev := <-ch:
			got = append(got, ev.Type)
		case <-timeout:
			t.Fatalf("timed out; got events: %v", got)
		}
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSessionLogTailerSkipsPreexistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	logPath := filepath.Join(dir, "20260101T000000Z-session.jsonl")
	pre := []byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"task_start","data":{"task_name":"old"}}` + "\n")
	if err := os.WriteFile(logPath, pre, 0o644); err != nil {
		t.Fatal(err)
	}

	broker := NewBroker()
	ch, unsub := broker.Subscribe()
	defer unsub()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tailer := NewSessionLogTailer(dir, broker, nil, 20*time.Millisecond)
	go tailer.Run(ctx)

	select {
	case ev := <-ch:
		t.Fatalf("unexpected event from pre-existing file: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}
