package faultfixture_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/faultfixture"
	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/stretchr/testify/require"
)

func nativeMCPAdapter(t *testing.T) *faultfixture.MCPAdapter {
	t.Helper()
	adapter, err := faultfixture.NewMCPAdapter(faultfixture.Envelope{
		Eval: []byte("schemaVersion: \"2.0\"\nscenario: native-transport\n"),
		Kind: "mcp", Name: "inventory", Tool: "read",
		Responses: []faultfixture.Fragment{{Format: faultfixture.JSON, Data: []byte(
			`{"match":{"id":"known"},"sequence":[{"error":"temporarily unavailable"},{"return":{"ready":true}}]}`,
		)}},
	}, t.TempDir())
	require.NoError(t, err)
	return adapter
}

func nativeCall(id, arguments string) string {
	return `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"read","arguments":` + arguments + `}` + id + "}\n"
}

func nativeServe(ctx context.Context, adapter *faultfixture.MCPAdapter, input string, writer io.Writer, observer faultfixture.Observer) error {
	cfg := &mcpmock.Config{Name: "inventory", Tools: map[string]mcpmock.Tool{
		"read": {Responses: []mcpmock.Response{{Return: "must not fall through"}}},
	}}
	return mcpmock.ServeStdioWithDispatch(ctx, cfg, strings.NewReader(input), writer, nil,
		func(ctx context.Context, request *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
			return true, adapter.Invoke(ctx, request, observer, deliver)
		})
}

func TestNativeMCPTransportUsesActualFiniteAdapter(t *testing.T) {
	adapter := nativeMCPAdapter(t)
	var wire bytes.Buffer
	var allocated, delivered []int
	observer := func(matcher, step int, transition faultfixture.Transition) error {
		require.Equal(t, 0, matcher)
		switch transition {
		case faultfixture.Reserved:
			allocated = append(allocated, step)
		case faultfixture.Delivered:
			delivered = append(delivered, step)
			require.Equal(t, len(delivered), strings.Count(wire.String(), "\n"),
				"native emission must precede a delivered observation")
		}
		return nil
	}
	input := nativeCall(`,"id":0`, `{"id":"known"}`) + nativeCall(`,"id":"second"`, `{"id":"known"}`)
	require.NoError(t, nativeServe(context.Background(), adapter, input, &wire, observer))
	require.Equal(t, []int{0, 1}, allocated)
	require.Equal(t, []int{0, 1}, delivered)
	require.Contains(t, wire.String(), `"id":0`)
	require.Contains(t, wire.String(), `"id":"second"`)
	require.Contains(t, wire.String(), `"isError":true`)
	require.Contains(t, wire.String(), `ready`)
	require.NotContains(t, wire.String(), "must not fall through")
	require.ErrorIs(t, nativeServe(context.Background(), adapter,
		nativeCall(`,"id":3`, `{"id":"known"}`), &wire, nil), faultsequence.ErrExhausted)
	require.Equal(t, 2, strings.Count(wire.String(), "\n"))
}

type nativeTransportWriter func([]byte) (int, error)

func (write nativeTransportWriter) Write(data []byte) (int, error) { return write(data) }

func TestNativeMCPTransportUndeliveredReservationsRemainConsumed(t *testing.T) {
	for _, mode := range []string{"cancel", "writer", "short-write"} {
		t.Run(mode, func(t *testing.T) {
			adapter := nativeMCPAdapter(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reserved, delivered := 0, 0
			observer := func(_, step int, transition faultfixture.Transition) error {
				if transition == faultfixture.Reserved {
					require.Equal(t, 0, step)
					reserved++
					if mode == "cancel" {
						cancel()
					}
				}
				if transition == faultfixture.Delivered {
					delivered++
				}
				return nil
			}
			writerErr := errors.New("native pipe unavailable")
			writer := nativeTransportWriter(func(data []byte) (int, error) {
				if mode == "short-write" {
					return len(data) - 1, nil
				}
				return 0, writerErr
			})
			err := nativeServe(ctx, adapter, nativeCall(`,"id":0`, `{"id":"known"}`), writer, observer)
			switch mode {
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
			case "short-write":
				require.ErrorIs(t, err, io.ErrShortWrite)
			default:
				require.ErrorIs(t, err, writerErr)
			}
			require.Equal(t, 1, reserved)
			require.Zero(t, delivered)
			var wire bytes.Buffer
			require.NoError(t, nativeServe(context.Background(), adapter,
				nativeCall(`,"id":1`, `{"id":"known"}`), &wire, nil))
			require.Contains(t, wire.String(), "ready")
			require.NotContains(t, wire.String(), "temporarily unavailable")
		})
	}
}

func TestNativeMCPTransportNotificationAndUnmatchedDoNotConsume(t *testing.T) {
	adapter := nativeMCPAdapter(t)
	var wire bytes.Buffer
	require.NoError(t, nativeServe(context.Background(), adapter, nativeCall("", `{"id":"known"}`), &wire, nil))
	require.Empty(t, wire.String())
	err := nativeServe(context.Background(), adapter, nativeCall(`,"id":1`, `{"token":"private-value"}`), &wire, nil)
	require.ErrorIs(t, err, faultfixture.ErrUnmatched)
	require.NotContains(t, err.Error(), "private-value")
	require.Empty(t, wire.String())
	require.NoError(t, nativeServe(context.Background(), adapter, nativeCall(`,"id":null`, `{"id":"known"}`), &wire, nil))
	require.Contains(t, wire.String(), "temporarily unavailable")
	require.Contains(t, wire.String(), `"id":null`)
}
