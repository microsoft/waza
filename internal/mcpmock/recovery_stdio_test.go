package mcpmock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

type reservationHandler struct{ reserved chan struct{} }

func (h reservationHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h reservationHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "MCP mock sequence step reserved" {
		select {
		case h.reserved <- struct{}{}:
		default:
		}
	}
	return nil
}
func (h reservationHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h reservationHandler) WithGroup(string) slog.Handler      { return h }

func TestRecoveryStdioCancellationThenRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	t.Cleanup(func() {
		require.NoError(t, send.Close())
		require.NoError(t, receive.Close())
	})
	reserved := make(chan struct{}, 2)
	cfg := &Config{Name: "dependency", Tools: map[string]Tool{
		"request": {Responses: []Response{{Sequence: []ResponseStep{
			{DelayMS: 300000, Error: "must be canceled"},
			{Return: "recovered"},
		}}}},
	}}
	done := make(chan error, 1)
	go func() {
		done <- ServeCancelableStdio(ctx, cfg, input, output, slog.New(reservationHandler{reserved}))
	}()
	write := func(value string) {
		t.Helper()
		_, err := io.WriteString(send, value+"\n")
		require.NoError(t, err)
	}
	write(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"request"},"id":"slow"}`)
	select {
	case <-reserved:
	case <-ctx.Done():
		t.Fatal("first request never reserved its delayed step")
	}
	write(`{"jsonrpc":"2.0","method":"` + ownedMCPCancelNotification + `","params":{"requestId":"unknown"}}`)
	write(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"request"}}`)
	write(`{"jsonrpc":"2.0","method":"` + ownedMCPCancelNotification + `","params":{"requestId":"slow"}}`)
	write(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"request"},"id":2}`)
	var response jsonrpc.Response
	require.NoError(t, json.NewDecoder(receive).Decode(&response))
	require.JSONEq(t, "2", string(response.ID))
	data, err := json.Marshal(response.Result)
	require.NoError(t, err)
	require.Contains(t, string(data), "recovered")
	require.NotContains(t, string(data), "isError")
	require.NoError(t, send.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("stdio did not join its reader and worker")
	}
}

func TestRecoveryStdioRootCancellationClosesBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()    //nolint:errcheck
	defer receive.Close() //nolint:errcheck
	done := make(chan error, 1)
	go func() { done <- ServeCancelableStdio(ctx, &Config{Name: "dependency"}, input, output, nil) }()
	cancel()
	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.Canceled), "%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation left a blocked reader")
	}
}
