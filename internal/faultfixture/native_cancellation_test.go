package faultfixture_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/faultfixture"
	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/stretchr/testify/require"
)

func TestActualNativeCancellationConsumesPreparedStepWithoutDelivery(t *testing.T) {
	adapter, err := faultfixture.NewMCPAdapter(faultfixture.Envelope{
		Eval: []byte("schemaVersion: \"2.0\"\nscenario: native-cancellation\n"),
		Kind: "mcp", Name: "inventory", Tool: "read",
		Responses: []faultfixture.Fragment{{Format: faultfixture.JSON, Data: []byte(
			`{"sequence":[{"return":"must not emit","delay_ms":60000},{"return":"next"}]}`,
		)}},
	}, t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input, clientWrite := io.Pipe()
	clientRead, output := io.Pipe()
	defer func() {
		require.NoError(t, clientWrite.Close())
		require.NoError(t, clientRead.Close())
	}()
	reserved := make(chan struct{})
	var mu sync.Mutex
	var transitions []faultfixture.Transition
	prepare := func(ctx context.Context, request *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
		pending, err := adapter.Prepare(ctx, request, func(_, step int, transition faultfixture.Transition) error {
			mu.Lock()
			transitions = append(transitions, transition)
			mu.Unlock()
			if transition == faultfixture.Reserved {
				if step != 0 {
					return io.ErrUnexpectedEOF
				}
				close(reserved)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		return pending.Deliver, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- mcpmock.ServeOwnedStdioWithPrepare(ctx,
			&mcpmock.Config{Name: "inventory", Tools: map[string]mcpmock.Tool{"read": {}}},
			input, output, nil, prepare, 2)
	}()
	type receivedWire struct {
		data []byte
		err  error
	}
	wire := make(chan receivedWire, 1)
	go func() {
		data, err := io.ReadAll(clientRead)
		wire <- receivedWire{data: data, err: err}
	}()
	_, err = io.WriteString(clientWrite, nativeCall(`,"id":"first"`, `{}`))
	require.NoError(t, err)
	select {
	case <-reserved:
	case <-ctx.Done():
		t.Fatal("native admission did not allocate before cancellation")
	}
	start := time.Now()
	_, err = io.WriteString(clientWrite,
		"{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":\"first\"}}\n") //nolint:misspell // Exact MCP protocol method.
	require.NoError(t, err)
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.Less(t, time.Since(start), 2*time.Second, "native notification must interrupt a long finite delay")
	case <-ctx.Done():
		t.Fatal("owned native cancellation failed to stop and join dispatch")
	}
	select {
	case received := <-wire:
		require.NoError(t, received.err)
		require.Empty(t, received.data, "canceled allocation must not emit a success or cancellation payload")
	case <-ctx.Done():
		t.Fatal("owned native cancellation did not close output")
	}
	mu.Lock()
	require.Equal(t, []faultfixture.Transition{faultfixture.Reserved, faultfixture.Canceled}, transitions)
	mu.Unlock()
	require.NoError(t, adapter.Invoke(context.Background(),
		&jsonrpc.Request{JSONRPC: "2.0", Method: "tools/call", Params: []byte(`{"name":"read"}`), ID: []byte(`1`)},
		nil, func(response *jsonrpc.Response) error {
			// This is the same attempt, not a reset or inferred SDK association.
			result, ok := response.Result.(map[string]any)
			require.True(t, ok)
			content, ok := result["content"].([]map[string]string)
			require.True(t, ok)
			require.Len(t, content, 1)
			require.Equal(t, `"next"`, content[0]["text"])
			return nil
		}))
}
