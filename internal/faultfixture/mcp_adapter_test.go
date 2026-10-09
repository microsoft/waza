package faultfixture

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

func mcpAdapterEnvelope(sources ...string) Envelope {
	envelope := Envelope{
		Eval: []byte("schemaVersion: \"2.0\"\nscenario: inventory\n"),
		Kind: "mcp", Name: "inventory", Tool: "read",
	}
	for _, source := range sources {
		envelope.Responses = append(envelope.Responses, Fragment{Format: JSON, Data: []byte(source)})
	}
	return envelope
}

func mcpAdapterRequest(arguments string) *jsonrpc.Request {
	return &jsonrpc.Request{
		JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage(`"native-request"`),
		Params: json.RawMessage(`{"name":"read","arguments":` + arguments + `}`),
	}
}

func mcpAdapterResult(t *testing.T, response *jsonrpc.Response) map[string]any {
	t.Helper()
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	return result
}

func mcpAdapterText(t *testing.T, response *jsonrpc.Response) string {
	t.Helper()
	content, ok := mcpAdapterResult(t, response)["content"].([]map[string]string)
	require.True(t, ok)
	require.Len(t, content, 1)
	return content[0]["text"]
}

func TestMCPAdapterNativeDeliveryAndFirstMatcherExhaustion(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
		`{"sequence":[{"error":"permission denied"},{"return":{"ready":true}}]}`,
		`{"return":"must not fall through"}`,
	), t.TempDir())
	require.NoError(t, err)
	var transitions []Transition
	observer := func(matcher, step int, transition Transition) error {
		require.Equal(t, 0, matcher)
		transitions = append(transitions, transition)
		return nil
	}
	var delivered []*jsonrpc.Response
	deliver := func(response *jsonrpc.Response) error {
		require.JSONEq(t, `"native-request"`, string(response.ID))
		delivered = append(delivered, response)
		return nil
	}
	require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), observer, deliver))
	require.Equal(t, true, mcpAdapterResult(t, delivered[0])["isError"])
	require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), observer, deliver))
	require.NotContains(t, mcpAdapterResult(t, delivered[1]), "isError")
	require.ErrorIs(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), observer, deliver), faultsequence.ErrExhausted)
	require.Len(t, delivered, 2, "exhaustion must not manufacture an MCP response")
	require.Equal(t, []Transition{Reserved, Delivered, Reserved, Delivered, Failed}, transitions)
}

func TestMCPAdapterCancellationAndDeliveryFailureStayOperational(t *testing.T) {
	for _, mode := range []string{"before", "after", "writer", "observer", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
				`{"sequence":[{"return":null,"delay_ms":9223372036854},{"return":"second"}]}`,
			), t.TempDir())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reserved := false
			callbackErr := errors.New("private observation failed")
			observer := func(_, step int, transition Transition) error {
				if transition == Reserved {
					reserved = true
					require.Equal(t, 0, step)
					switch mode {
					case "after":
						cancel()
					case "observer":
						return callbackErr
					}
				}
				return nil
			}
			if mode == "before" {
				cancel()
			}
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer stop()
			}
			if mode == "writer" {
				// Use an immediate step so this exercises the actual delivery boundary.
				adapter, err = NewMCPAdapter(mcpAdapterEnvelope(`{"sequence":[{"return":null},{"return":"second"}]}`), t.TempDir())
				require.NoError(t, err)
			}
			deliveries := 0
			err = adapter.Invoke(ctx, mcpAdapterRequest(`{}`), observer, func(*jsonrpc.Response) error {
				deliveries++
				return io.ErrClosedPipe
			})
			switch mode {
			case "before", "after":
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, deliveries)
			case "deadline":
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Zero(t, deliveries)
			case "writer":
				require.ErrorIs(t, err, io.ErrClosedPipe)
				require.Equal(t, 1, deliveries)
			case "observer":
				require.ErrorIs(t, err, callbackErr)
				require.Zero(t, deliveries)
			}
			if mode == "before" {
				require.False(t, reserved)
			}
			if reserved {
				var text string
				require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(response *jsonrpc.Response) error {
					text = mcpAdapterText(t, response)
					return nil
				}))
				require.Equal(t, `"second"`, text)
			}
		})
	}
}

func TestMCPAdapterUnmatchedRedactionAndImmutableSnapshots(t *testing.T) {
	envelope := mcpAdapterEnvelope(`{"match":{"id":"known"},"sequence":[{"return":"first"},{"return":"second"}]}`)
	adapter, err := NewMCPAdapter(envelope, t.TempDir())
	require.NoError(t, err)
	envelope.Responses[0].Data[0] = '!'
	envelope.Eval[0] = '!'
	deliveries := 0
	err = adapter.Invoke(context.Background(), mcpAdapterRequest(`{"token":"private-argument"}`), nil, func(*jsonrpc.Response) error {
		deliveries++
		return nil
	})
	require.ErrorIs(t, err, ErrUnmatched)
	require.NotContains(t, err.Error(), "private-argument")
	require.Zero(t, deliveries)
	request := mcpAdapterRequest(`{"id":"known"}`)
	require.NoError(t, adapter.Invoke(context.Background(), request, nil, func(response *jsonrpc.Response) error {
		response.ID[0] = '!'
		content, ok := mcpAdapterResult(t, response)["content"].([]map[string]string)
		require.True(t, ok)
		require.Len(t, content, 1)
		content[0]["text"] = "mutated"
		return nil
	}))
	require.Equal(t, `"native-request"`, string(request.ID))
	require.NoError(t, adapter.Invoke(context.Background(), request, nil, func(response *jsonrpc.Response) error {
		require.Equal(t, `"second"`, mcpAdapterText(t, response))
		require.Equal(t, `"native-request"`, string(response.ID))
		return nil
	}))
}

func TestMCPAdapterSameOwnerConfigurationCannotReset(t *testing.T) {
	root := t.TempDir()
	first := mcpAdapterEnvelope(`{"sequence":[{"return":"one"}]}`)
	_, err := NewMCPAdapter(first, root)
	require.NoError(t, err)
	_, err = NewMCPAdapter(first, root)
	require.NoError(t, err)
	for _, source := range []string{
		`{"sequence":[{"return":"changed"}]}`,
		`{"sequence":[{"return":"one"},{"return":"two"}]}`,
	} {
		_, err := NewMCPAdapter(mcpAdapterEnvelope(source), root)
		require.ErrorContains(t, err, "configuration changed")
	}
}

func TestMCPAdapterToolOwnershipConcurrencyAndPrivateReset(t *testing.T) {
	root := t.TempDir()
	envelope := mcpAdapterEnvelope(`{"sequence":[{"return":"one"},{"return":"two"}]}`)
	adapter, err := NewMCPAdapter(envelope, root)
	require.NoError(t, err)
	other := mcpAdapterEnvelope(`{"sequence":[{"return":"independent"}]}`)
	other.Tool = "other"
	second, err := NewMCPAdapter(other, root)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(*jsonrpc.Response) error { return nil }); err != nil {
				t.Errorf("concurrent allocation: %v", err)
			}
		})
	}
	wg.Wait()
	require.ErrorIs(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(*jsonrpc.Response) error { return nil }), faultsequence.ErrExhausted)
	request := mcpAdapterRequest(`{}`)
	request.Params = json.RawMessage(`{"name":"other","arguments":{}}`)
	require.NoError(t, second.Invoke(context.Background(), request, nil, func(*jsonrpc.Response) error { return nil }))
	reset, err := NewMCPAdapter(envelope, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, reset.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(*jsonrpc.Response) error { return nil }))
}

func TestMCPAdapterRejectsAllSourcesBeforeBinding(t *testing.T) {
	for _, invalid := range []string{
		`{"sequence":[{"return":null,"delay_ms":-1}]}`,
		`{"match_schema":{"$ref":"file:///must-not-read"},"return":null}`,
		`{"match_schema":{"$ref":"https://schema.invalid/must-not-fetch"},"return":null}`,
	} {
		root := t.TempDir()
		_, err := NewMCPAdapter(mcpAdapterEnvelope(`{"return":"first catchall"}`, invalid), root)
		require.Error(t, err)
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	_, err := NewMCPAdapter(mcpAdapterEnvelope(`{"return":null}`), filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	_, err = NewMCPAdapter(mcpAdapterEnvelope(`{"return":null}`), ".")
	require.Error(t, err)
}

func TestMCPAdapterAdmissionNeverDeliversInvalidRequests(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(`{"sequence":[{"return":null}]}`), t.TempDir())
	require.NoError(t, err)
	for _, request := range []*jsonrpc.Request{
		nil,
		{Method: "tools/list"},
		{Method: "tools/call", Params: json.RawMessage(`invalid`)},
		{Method: "tools/call", Params: json.RawMessage(`{"name":"read","arguments":[]}`)},
		{Method: "tools/call", Params: json.RawMessage(`{"name":"other","arguments":{}}`)},
	} {
		require.Error(t, adapter.Invoke(context.Background(), request, nil, func(*jsonrpc.Response) error {
			t.Error("operational rejection delivered a fabricated result")
			return nil
		}))
	}
	require.Error(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, nil))
	require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(*jsonrpc.Response) error { return nil }))
}
