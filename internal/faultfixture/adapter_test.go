package faultfixture

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestAdaptersDelayBeforeDeliveryAndPreserveCompletionFailure(t *testing.T) {
	for _, kind := range []string{"command", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			var invoke func(Observer, func() error) error
			root := t.TempDir()
			if kind == "command" {
				adapter, err := NewCommandAdapter(commandEnvelopeForTest(
					`{"args":[],"sequence":[{"stdout":"ready","delay_ms":20}]}`,
				), root, root)
				require.NoError(t, err)
				invoke = func(observer Observer, deliver func() error) error {
					return adapter.Invoke(context.Background(), nil, root, observer, func(CommandOutput) error { return deliver() })
				}
			} else {
				adapter, err := NewMCPAdapter(mcpAdapterEnvelope(
					`{"sequence":[{"return":"ready","delay_ms":20}]}`,
				), root)
				require.NoError(t, err)
				invoke = func(observer Observer, deliver func() error) error {
					return adapter.Invoke(context.Background(), mcpAdapterRequest(`{}`), observer, func(*jsonrpc.Response) error { return deliver() })
				}
			}
			completionErr := errors.New("completion receipt unavailable")
			var transitions []Transition
			observer := func(matcher, step int, transition Transition) error {
				require.Equal(t, 0, matcher)
				require.Equal(t, 0, step)
				transitions = append(transitions, transition)
				if transition == Delivered {
					return completionErr
				}
				return nil
			}
			start := time.Now()
			deliveries := 0
			err := invoke(observer, func() error {
				require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
				deliveries++
				return nil
			})
			require.ErrorIs(t, err, completionErr)
			require.Equal(t, 1, deliveries)
			require.Equal(t, []Transition{Reserved, Delivered}, transitions)
			require.ErrorIs(t, invoke(nil, func() error {
				t.Fatal("exhaustion must not deliver a second response")
				return nil
			}), faultsequence.ErrExhausted)
		})
	}
}

func TestDecodeEnvelopeRejectsUnknownDuplicateAndTrailingConfiguration(t *testing.T) {
	valid, err := json.Marshal(mcpAdapterEnvelope(`{"sequence":[{"return":null}]}`))
	require.NoError(t, err)
	_, err = DecodeEnvelope(valid)
	require.NoError(t, err)
	for _, data := range []string{
		string(valid[:len(valid)-1]) + `,"unexpected":true}`,
		string(valid[:len(valid)-1]) + `,"kind":"command"}`,
		string(valid) + `{}`,
		`null`,
	} {
		_, err := DecodeEnvelope([]byte(data))
		require.Error(t, err)
	}
}
