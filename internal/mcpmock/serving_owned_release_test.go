package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestOwnedMCPReleaseRemovesCompletedCallBeforeCancel(t *testing.T) {
	for _, id := range []string{`1`, `null`, `"finished"`} {
		t.Run(id, func(t *testing.T) {
			var output bytes.Buffer
			serving := &ownedMCPServing{
				ctx: t.Context(), capacity: 1, active: make(map[string]*ownedMCPCall),
				transport: jsonrpc.NewTransport(bytes.NewReader(nil), &output),
				failures:  make(chan error, 1), finished: make(chan struct{}, 1),
			}
			call, err := serving.admit(json.RawMessage(id))
			require.NoError(t, err)
			cancelEntered, resumeCancel, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(resumeCancel) }) }
			t.Cleanup(func() {
				resume()
				awaitOwnedSignal(t, done)
			})
			originalCancel := call.cancel
			var firstCancel atomic.Bool
			call.cancel = func() {
				// Pause release after response delivery. A stale notification
				// must not find this call, even while its context cleanup waits.
				if firstCancel.CompareAndSwap(false, true) {
					close(cancelEntered)
					<-resumeCancel
				}
				originalCancel()
			}
			go func() {
				serving.respond(call, nil, func(_ context.Context, deliver func(*jsonrpc.Response) error) error {
					return deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: json.RawMessage(id), Result: "committed"})
				})
				close(done)
			}()
			awaitOwnedSignal(t, cancelEntered)
			var response jsonrpc.Response
			require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response))
			require.Equal(t, json.RawMessage(id), response.ID)
			require.Equal(t, "committed", response.Result)
			require.False(t, serving.cancelNativeRequest(json.RawMessage(`{"requestId":`+id+`}`)),
				"a completed call's cancellation must not stop serving")

			reused, err := serving.admit(json.RawMessage(id))
			require.NoError(t, err, "release must free the native ID and capacity before canceling its context")
			defer serving.release(reused)
			require.NotSame(t, call, reused)
			require.NoError(t, reused.ctx.Err())
			resume()
			awaitOwnedSignal(t, done)
			require.ErrorIs(t, call.ctx.Err(), context.Canceled)
			serving.mu.Lock()
			active, inFlight := serving.active[reused.key], serving.inFlight
			serving.mu.Unlock()
			require.Same(t, reused, active, "old cleanup must not remove the reused native ID")
			require.Equal(t, 1, inFlight)
			require.NoError(t, reused.ctx.Err())
			select {
			case err := <-serving.failures:
				t.Fatalf("completed-call cancellation stopped serving: %v", err)
			default:
			}

			require.True(t, serving.cancelNativeRequest(json.RawMessage(`{"requestId":`+id+`}`)),
				"a genuinely active reused ID must still cancel")
			require.ErrorIs(t, reused.ctx.Err(), context.Canceled)
			require.ErrorIs(t, awaitOwnedSignal(t, serving.failures), context.Canceled)
		})
	}
}
