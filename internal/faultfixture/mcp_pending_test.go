package faultfixture

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestMCPPreparedCallsReserveBeforeDeliveryInAdmissionOrder(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
		`{"sequence":[{"return":"first"},{"return":"second"}]}`,
	), t.TempDir())
	require.NoError(t, err)
	var allocated []int
	observer := func(_, step int, transition Transition) error {
		if transition == Reserved {
			allocated = append(allocated, step)
		}
		return nil
	}
	request := mcpAdapterRequest(`{}`)
	first, err := adapter.Prepare(context.Background(), request, observer)
	require.NoError(t, err)
	second, err := adapter.Prepare(context.Background(), request, observer)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, allocated)
	request.ID[0] = '!'
	var actual []string
	deliver := func(response *jsonrpc.Response) error {
		require.JSONEq(t, `"native-request"`, string(response.ID))
		actual = append(actual, mcpAdapterText(t, response))
		return nil
	}
	require.NoError(t, second.Deliver(context.Background(), deliver))
	require.NoError(t, first.Deliver(context.Background(), deliver))
	require.Equal(t, []string{`"second"`, `"first"`}, actual,
		"allocation order must not be advertised as delivery order")
	require.ErrorContains(t, first.Deliver(context.Background(), deliver), "already attempted")
	require.Len(t, actual, 2)
}

func TestMCPPreparedCancellationAndInvalidCallbackConsumeOnce(t *testing.T) {
	for _, mode := range []string{"cancel", "nil-delivery"} {
		t.Run(mode, func(t *testing.T) {
			adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
				`{"sequence":[{"return":"first"},{"return":"second"}]}`,
			), t.TempDir())
			require.NoError(t, err)
			var transitions []Transition
			pending, err := adapter.Prepare(context.Background(), mcpAdapterRequest(`{}`),
				func(_, _ int, transition Transition) error {
					transitions = append(transitions, transition)
					return nil
				})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var deliver func(*jsonrpc.Response) error
			if mode == "cancel" {
				cancel()
				deliver = func(*jsonrpc.Response) error {
					t.Fatal("canceled call must not emit")
					return nil
				}
			}
			err = pending.Deliver(ctx, deliver)
			if mode == "cancel" {
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, []Transition{Reserved, Canceled}, transitions)
			} else {
				require.ErrorContains(t, err, "delivery callback")
				require.Equal(t, []Transition{Reserved, Failed}, transitions)
			}
			require.ErrorContains(t, pending.Deliver(context.Background(), func(*jsonrpc.Response) error { return nil }), "already attempted")
			require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil,
				func(response *jsonrpc.Response) error {
					require.Equal(t, `"second"`, mcpAdapterText(t, response))
					return nil
				}))
		})
	}
}

func TestMCPPreparedPointerAllowsOnlyOneConcurrentDelivery(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(`{"sequence":[{"return":null}]}`), t.TempDir())
	require.NoError(t, err)
	pending, err := adapter.Prepare(context.Background(), mcpAdapterRequest(`{}`), nil)
	require.NoError(t, err)
	var writes atomic.Int64
	var workers sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		workers.Go(func() {
			results <- pending.Deliver(context.Background(), func(*jsonrpc.Response) error {
				writes.Add(1)
				return nil
			})
		})
	}
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorContains(t, err, "already attempted")
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, int64(1), writes.Load())
}

func TestMCPInvokeRejectsMissingDeliveryBeforeReservation(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(`{"sequence":[{"return":null}]}`), t.TempDir())
	require.NoError(t, err)
	require.Error(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, nil))
	pending, err := adapter.Prepare(context.Background(), mcpAdapterRequest(`{}`), nil)
	require.NoError(t, err)
	require.Equal(t, 0, pending.observedStep)
	require.NoError(t, pending.Deliver(context.Background(), func(response *jsonrpc.Response) error {
		require.Equal(t, json.RawMessage(`"native-request"`), response.ID)
		return nil
	}))
	var absent *PendingMCPCall
	require.ErrorContains(t, absent.Deliver(context.Background(), nil), "prepared call")
}

func TestMCPPreparedCallCannotReplaceCanceledAdmissionContext(t *testing.T) {
	adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
		`{"sequence":[{"return":"first"},{"return":"second"}]}`,
	), t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	pending, err := adapter.Prepare(ctx, mcpAdapterRequest(`{}`), nil)
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, pending.Deliver(context.Background(), func(*jsonrpc.Response) error {
		t.Fatal("a replacement context must not revive the canceled native call")
		return nil
	}), context.Canceled)
	require.NoError(t, adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), nil, func(response *jsonrpc.Response) error {
		require.Equal(t, `"second"`, mcpAdapterText(t, response))
		return nil
	}))
}
