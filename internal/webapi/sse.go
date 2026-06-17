package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSEEvent is the wire-format event sent to dashboard SSE clients.
// It matches the contract expected by web/src/hooks/useSSE.ts.
type SSEEvent struct {
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// Allowed live event types. Other event types are dropped so the wire
// contract stays narrow.
var liveEventTypes = map[string]struct{}{
	"task_start":    {},
	"task_complete": {},
	"grader_result": {},
	"run_complete":  {},
}

// IsLiveEventType reports whether the given event type is published to
// SSE subscribers.
func IsLiveEventType(t string) bool {
	_, ok := liveEventTypes[t]
	return ok
}

// Default subscriber buffer size. Slow clients are dropped instead of
// blocking the publisher.
const sseSubscriberBuffer = 64

// Broker fans out SSEEvents to all current SSE subscribers.
//
// The zero value is not ready for use; call NewBroker. Broker is safe for
// concurrent use.
type Broker struct {
	mu          sync.Mutex
	subscribers map[chan SSEEvent]struct{}
}

// NewBroker creates a Broker with no subscribers.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[chan SSEEvent]struct{})}
}

// Subscribe registers a new subscriber and returns the channel it should
// read from along with an unsubscribe func. The channel is buffered;
// when full, the oldest events are dropped for that subscriber so a
// slow client never blocks the publisher.
func (b *Broker) Subscribe() (<-chan SSEEvent, func()) {
	ch := make(chan SSEEvent, sseSubscriberBuffer)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish sends an event to all current subscribers. Events whose Type is
// not a recognized live event are dropped. Subscribers whose buffer is
// full have their oldest event dropped to make room — the publisher is
// never blocked.
func (b *Broker) Publish(ev SSEEvent) {
	if !IsLiveEventType(ev.Type) {
		return
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// Drop oldest, then enqueue the new event. Best-effort.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// SubscriberCount returns the current number of subscribers. Primarily
// useful for tests.
func (b *Broker) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

// HandleEvents serves a long-lived SSE stream of live run events.
//
// Behavior:
//   - Sends `Content-Type: text/event-stream` (plus no-cache, keep-alive,
//     and X-Accel-Buffering: no for proxies).
//   - Sends an initial `: connected` comment so EventSource fires onopen.
//   - Sends a heartbeat comment every 15 seconds so idle connections
//     don't get dropped by intermediaries.
//   - Closes when the request context is canceled (client disconnect or
//     server shutdown).
func (h *Handlers) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if h.broker == nil {
		writeError(w, http.StatusServiceUnavailable, "live events broker not configured")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	ch, unsubscribe := h.broker.Subscribe()
	defer unsubscribe()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// SetBroker attaches a Broker so /api/events can stream live events.
func (h *Handlers) SetBroker(b *Broker) { h.broker = b }

// Broker exposes the configured broker. May be nil if SSE is disabled.
func (h *Handlers) Broker() *Broker { return h.broker }

// PublishLiveEvent is a convenience helper that publishes an event when
// a broker is configured. Safe to call when no broker is set.
func (h *Handlers) PublishLiveEvent(ev SSEEvent) {
	if h.broker == nil {
		return
	}
	h.broker.Publish(ev)
}
