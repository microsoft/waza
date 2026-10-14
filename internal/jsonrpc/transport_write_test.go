package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type transportWriteFunc func([]byte) (int, error)

func (write transportWriteFunc) Write(data []byte) (int, error) {
	return write(data)
}

func TestTransportWritesRequireCompleteNativeEmission(t *testing.T) {
	writerErr := errors.New("native writer unavailable")
	for _, kind := range []string{"response", "notification"} {
		for _, mode := range []string{"complete", "short", "zero", "error", "partial-error"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				var actual []byte
				transport := NewTransport(bytes.NewReader(nil), transportWriteFunc(func(data []byte) (int, error) {
					actual = bytes.Clone(data)
					switch mode {
					case "short":
						return len(data) - 1, nil
					case "zero":
						return 0, nil
					case "error":
						return 0, writerErr
					case "partial-error":
						return len(data) - 1, writerErr
					default:
						return len(data), nil
					}
				}))
				var err error
				if kind == "response" {
					err = transport.WriteResponse(&Response{JSONRPC: "2.0", ID: json.RawMessage(`0`), Result: "ready"})
				} else {
					err = transport.WriteNotification(&Notification{JSONRPC: "2.0", Method: "ready"})
				}
				require.NotEmpty(t, actual)
				require.Equal(t, byte('\n'), actual[len(actual)-1])
				require.True(t, json.Valid(bytes.TrimSpace(actual)))
				switch mode {
				case "short", "zero":
					require.ErrorIs(t, err, io.ErrShortWrite)
				case "error", "partial-error":
					require.ErrorIs(t, err, writerErr)
				default:
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestTransportEncodingFailureDoesNotWrite(t *testing.T) {
	writes := 0
	transport := NewTransport(bytes.NewReader(nil), transportWriteFunc(func(data []byte) (int, error) {
		writes++
		return len(data), nil
	}))
	require.Error(t, transport.WriteResponse(&Response{JSONRPC: "2.0", Result: make(chan int)}))
	require.Error(t, transport.WriteNotification(&Notification{JSONRPC: "2.0", Params: make(chan int)}))
	require.Zero(t, writes)
}

func TestTransportContextCancellationIsCheckedAfterOutputLock(t *testing.T) {
	writes := 0
	transport := NewTransport(bytes.NewReader(nil), transportWriteFunc(func(data []byte) (int, error) {
		writes++
		return len(data), nil
	}))
	transport.writeMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- transport.WriteResponseContext(ctx, &Response{JSONRPC: "2.0", ID: json.RawMessage(`0`), Result: "must not emit"})
	}()
	cancel()
	transport.writeMu.Unlock()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Zero(t, writes)
	require.NoError(t, transport.WriteResponse(&Response{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: "legacy unchanged"}))
	require.Equal(t, 1, writes)
}
