package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

var errServingExhausted = errors.New("bounded native fixture exhausted")

// This bounded fake reserves before native delivery and records success only
// after the real writer returns. It deliberately has no adapter dependencies.
type boundedNativeDispatch struct {
	steps        []map[string]any
	reserved     int
	delivered    int
	calls        int
	afterReserve func() error
}

func (fake *boundedNativeDispatch) dispatch(ctx context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
	fake.calls++
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return false, err
	}
	if params.Name != "owned" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if fake.reserved == len(fake.steps) {
		return true, errServingExhausted
	}
	step := fake.steps[fake.reserved]
	fake.reserved++
	if fake.afterReserve != nil {
		if err := fake.afterReserve(); err != nil {
			return true, err
		}
	}
	if err := deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: step}); err != nil {
		return true, err
	}
	fake.delivered++
	return true, nil
}

func servingTestConfig() *Config {
	return &Config{Name: "native", Tools: map[string]Tool{
		"owned":  {Responses: []Response{{Return: "LEGACY FALLTHROUGH"}}},
		"legacy": {Responses: []Response{{Return: "legacy fixture"}}},
	}}
}

func servingTestResult(text string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
}

func servingTestCall(id string) string {
	return `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"owned","arguments":{}}` + id + "}\n"
}

func serveWithTestDispatch(ctx context.Context, input string, out io.Writer, dispatch ToolCallDispatch) error {
	return ServeStdioWithDispatch(ctx, servingTestConfig(), strings.NewReader(input), out, nil, dispatch)
}

type servingWriterFunc func([]byte) (int, error)

func (fn servingWriterFunc) Write(data []byte) (int, error) { return fn(data) }

type servingReaderFunc func([]byte) (int, error)

func (fn servingReaderFunc) Read(data []byte) (int, error) { return fn(data) }

func TestServeStdioWithDispatchNativeDelivery(t *testing.T) {
	for _, fixtureError := range []bool{false, true} {
		t.Run(map[bool]string{false: "return", true: "fixture error"}[fixtureError], func(t *testing.T) {
			step := servingTestResult("finite fixture")
			if fixtureError {
				step["isError"] = true
			}
			fake := &boundedNativeDispatch{steps: []map[string]any{step}}
			var wire bytes.Buffer
			writer := servingWriterFunc(func(data []byte) (int, error) {
				require.Equal(t, 1, fake.reserved)
				require.Zero(t, fake.delivered, "delivery must not be acknowledged before the native write")
				return wire.Write(data)
			})
			require.NoError(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":"native-id"`), writer, fake.dispatch))
			require.Equal(t, 1, fake.delivered)
			require.NotContains(t, wire.String(), "LEGACY FALLTHROUGH")
			var response jsonrpc.Response
			require.NoError(t, json.NewDecoder(&wire).Decode(&response))
			require.Equal(t, json.RawMessage(`"native-id"`), response.ID)
			require.Nil(t, response.Error)
			result, ok := response.Result.(map[string]any)
			require.True(t, ok, "tool result must be an object")
			content, ok := result["content"].([]any)
			require.True(t, ok, "tool content must be an array")
			require.Len(t, content, 1)
			entry, ok := content[0].(map[string]any)
			require.True(t, ok, "tool content entry must be an object")
			require.Equal(t, "finite fixture", entry["text"])
			require.Equal(t, fixtureError, result["isError"] == true)
		})
	}
}

func TestServeStdioWithDispatchOperationalErrorOwnsCall(t *testing.T) {
	for _, handled := range []bool{false, true} {
		for _, message := range []string{"unmatched", "exhausted", "canceled", "observer failure"} {
			t.Run(message+map[bool]string{true: "/owned", false: "/declined"}[handled], func(t *testing.T) {
				want := errors.New(message)
				dispatch := func(context.Context, *jsonrpc.Request, func(*jsonrpc.Response) error) (bool, error) {
					return handled, want
				}
				var out bytes.Buffer
				err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), &out, dispatch)
				require.ErrorIs(t, err, want)
				require.Empty(t, out.String(), "operational errors must not create a legacy result")
			})
		}
	}
}

func TestServeStdioWithDispatchExhaustionDoesNotFallThrough(t *testing.T) {
	fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
	var out bytes.Buffer
	err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`)+servingTestCall(`,"id":2`), &out, fake.dispatch)
	require.ErrorIs(t, err, errServingExhausted)
	require.Equal(t, 1, fake.reserved)
	require.Equal(t, 1, fake.delivered)
	require.Equal(t, 1, strings.Count(out.String(), "\n"))
	require.NotContains(t, out.String(), "LEGACY FALLTHROUGH")
}

func TestServeStdioWithDispatchWriterErrorSurfaces(t *testing.T) {
	fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
	want := errors.New("native writer failure")
	var writes int
	writer := servingWriterFunc(func([]byte) (int, error) {
		writes++
		return 0, want
	})
	err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), writer, fake.dispatch)
	require.ErrorIs(t, err, want)
	require.Equal(t, 1, fake.reserved)
	require.Zero(t, fake.delivered)
	require.Equal(t, 1, writes)
}

func TestServeStdioWithDispatchShortWriteDoesNotDeliver(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(map[bool]string{false: "short", true: "zero"}[zero], func(t *testing.T) {
			fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
			var out bytes.Buffer
			var writes int
			writer := servingWriterFunc(func(data []byte) (int, error) {
				writes++
				n := len(data) / 2
				if zero {
					n = 0
				}
				return out.Write(data[:n])
			})
			input := servingTestCall(`,"id":1`) + servingTestCall(`,"id":2`)
			err := serveWithTestDispatch(context.Background(), input, writer, fake.dispatch)
			require.ErrorIs(t, err, io.ErrShortWrite)
			require.Equal(t, 1, fake.calls, "short writes must stop native serving")
			require.Equal(t, 1, fake.reserved)
			require.Zero(t, fake.delivered)
			require.Equal(t, 1, writes, "no legacy fallback or retry")
			require.NotContains(t, out.String(), "LEGACY FALLTHROUGH")
		})
	}
}

func TestServeStdioWithDispatchPreservesWriterAndDispatchErrors(t *testing.T) {
	writerErr := errors.New("native writer failure")
	dispatchErr := errors.New("private dispatch failure")
	fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
	var writes int
	writer := servingWriterFunc(func([]byte) (int, error) {
		writes++
		return 0, writerErr
	})
	dispatch := func(ctx context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
		handled, err := fake.dispatch(ctx, req, deliver)
		require.ErrorIs(t, err, writerErr)
		return handled, dispatchErr
	}
	err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), writer, dispatch)
	require.ErrorIs(t, err, writerErr)
	require.ErrorIs(t, err, dispatchErr)
	require.Equal(t, 1, fake.reserved)
	require.Zero(t, fake.delivered)
	require.Equal(t, 1, writes, "neither error may trigger legacy fallback")
}

func TestServeStdioWithDispatchCannotSwallowWriterFailure(t *testing.T) {
	want := errors.New("native writer failure")
	writer := servingWriterFunc(func([]byte) (int, error) { return 0, want })
	dispatch := func(_ context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
		response := &jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: servingTestResult("one")}
		require.ErrorIs(t, deliver(response), want)
		require.ErrorIs(t, deliver(response), want, "a second attempt must not erase the original writer error")
		return true, nil
	}
	require.ErrorIs(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), writer, dispatch), want)
}

func TestServeStdioWithDispatchNotificationAndNullID(t *testing.T) {
	fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("null-ID fixture")}}
	var out bytes.Buffer
	require.NoError(t, serveWithTestDispatch(context.Background(), servingTestCall(""), &out, fake.dispatch))
	require.Zero(t, fake.calls)
	require.Zero(t, fake.reserved)
	require.Zero(t, fake.delivered)
	require.Empty(t, out.String())

	require.NoError(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":null`), &out, fake.dispatch))
	require.Equal(t, 1, fake.calls)
	require.Equal(t, 1, fake.reserved)
	require.Equal(t, 1, fake.delivered)
	var response jsonrpc.Response
	require.NoError(t, json.NewDecoder(&out).Decode(&response))
	require.Equal(t, json.RawMessage("null"), response.ID, "native transport distinguishes explicit null from an absent ID")
}

func TestServeStdioWithDispatchLegacyLifecycleAndUnconfiguredTool(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"initialize","id":1}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","method":"tools/list","id":2}` + "\n" +
		`{"jsonrpc":"2.0","method":"ping","id":3}` + "\n" +
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"legacy"},"id":4}` + "\n"
	fake := &boundedNativeDispatch{}
	var legacy, hooked bytes.Buffer
	ServeStdio(context.Background(), servingTestConfig(), strings.NewReader(input), &legacy, slog.Default())
	require.NoError(t, serveWithTestDispatch(context.Background(), input, &hooked, fake.dispatch))
	require.Equal(t, legacy.String(), hooked.String(), "including the native method-not-found ping response")
	require.Equal(t, 1, fake.calls, "only tools/call may reach dispatch")
	require.Zero(t, fake.reserved)
	require.Contains(t, hooked.String(), "legacy fixture")
}

func TestServeStdioWithDispatchNilCompatibility(t *testing.T) {
	input := servingTestCall(`,"id":1`) + servingTestCall("") +
		servingTestCall(`,"id":null`) +
		`{"jsonrpc":"2.0","method":"tools/call","id":2,"params":{"name":"owned","arguments":"bad"}}` + "\n"
	var legacy, hooked bytes.Buffer
	ServeStdio(context.Background(), servingTestConfig(), strings.NewReader(input), &legacy, slog.Default())
	require.NoError(t, serveWithTestDispatch(context.Background(), input, &hooked, nil))
	require.Equal(t, legacy.String(), hooked.String())
}

func TestServeStdioWithDispatchCancellation(t *testing.T) {
	t.Run("before read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
		var out bytes.Buffer
		err := serveWithTestDispatch(ctx, servingTestCall(`,"id":1`), &out, fake.dispatch)
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, fake.calls)
		require.Zero(t, fake.reserved)
		require.Empty(t, out.String())
	})
	t.Run("during read before dispatch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		input := strings.NewReader(servingTestCall(`,"id":1`))
		reader := servingReaderFunc(func(data []byte) (int, error) {
			n, err := input.Read(data)
			cancel()
			return n, err
		})
		fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("one")}}
		var out bytes.Buffer
		err := ServeStdioWithDispatch(ctx, servingTestConfig(), reader, &out, nil, fake.dispatch)
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, fake.calls)
		require.Zero(t, fake.reserved)
		require.Empty(t, out.String())
	})
	t.Run("after reservation", func(t *testing.T) {
		for _, returnCancellation := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &boundedNativeDispatch{steps: []map[string]any{servingTestResult("never delivered")}}
			fake.afterReserve = func() error {
				cancel()
				if returnCancellation {
					return ctx.Err()
				}
				return nil // Also test a dispatch attempting delivery after cancellation.
			}
			var out bytes.Buffer
			err := serveWithTestDispatch(ctx, servingTestCall(`,"id":1`), &out, fake.dispatch)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, fake.reserved)
			require.Zero(t, fake.delivered)
			require.Empty(t, out.String(), "no fabricated cancellation payload")
		}
	})
}

func TestServeStdioWithDispatchRejectsInvalidDelivery(t *testing.T) {
	tests := []struct {
		name string
		make func(*jsonrpc.Request) *jsonrpc.Response
	}{
		{"nil", func(*jsonrpc.Request) *jsonrpc.Response { return nil }},
		{"no outcome", func(req *jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "2.0", ID: req.ID}
		}},
		{"both outcomes", func(req *jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: "bad", Error: jsonrpc.ErrInternalError(nil)}
		}},
		{"wrong version", func(req *jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "1.0", ID: req.ID, Result: "bad"}
		}},
		{"wrong ID", func(*jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "2.0", ID: json.RawMessage(`9`), Result: "bad"}
		}},
		{"missing ID", func(*jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "2.0", Result: "bad"}
		}},
		{"unmarshalable result", func(req *jsonrpc.Request) *jsonrpc.Response {
			return &jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: make(chan int)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			dispatch := func(_ context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
				// Even a buggy dispatch swallowing a delivery error cannot report serving success.
				_ = deliver(test.make(req))
				return true, nil
			}
			err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), &out, dispatch)
			require.Error(t, err)
			require.Empty(t, out.String())
		})
	}
}

func TestServeStdioWithDispatchContractViolations(t *testing.T) {
	for _, behavior := range []string{"no delivery", "unowned delivery", "double delivery"} {
		t.Run(behavior, func(t *testing.T) {
			var out bytes.Buffer
			dispatch := func(_ context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
				response := &jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Result: servingTestResult("one")}
				switch behavior {
				case "no delivery":
					return true, nil
				case "unowned delivery":
					require.NoError(t, deliver(response))
					return false, nil
				default:
					require.NoError(t, deliver(response))
					require.Error(t, deliver(response))
					return true, nil
				}
			}
			err := serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), &out, dispatch)
			require.Error(t, err)
			require.NotContains(t, out.String(), "LEGACY FALLTHROUGH")
			require.LessOrEqual(t, strings.Count(out.String(), "\n"), 1)
		})
	}
}

func TestServeStdioWithDispatchRejectsRetainedCallback(t *testing.T) {
	var retained func(*jsonrpc.Response) error
	dispatch := func(_ context.Context, _ *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
		retained = deliver
		return true, nil
	}
	var out bytes.Buffer
	require.Error(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), &out, dispatch))
	require.ErrorContains(t, retained(&jsonrpc.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: "late"}), "after dispatch returned")
	require.Empty(t, out.String())
}

func TestServeStdioWithDispatchNativeProtocolError(t *testing.T) {
	dispatch := func(_ context.Context, req *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (bool, error) {
		return true, deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: req.ID, Error: jsonrpc.ErrInvalidParams("fixture")})
	}
	var out bytes.Buffer
	require.NoError(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":1`), &out, dispatch))
	var response jsonrpc.Response
	require.NoError(t, json.NewDecoder(&out).Decode(&response))
	require.Nil(t, response.Result)
	require.Equal(t, jsonrpc.CodeInvalidParams, response.Error.Code)
}

func TestServeStdioWithDispatchReadPolicy(t *testing.T) {
	for _, input := range []string{"", servingTestCall(`,"id":1`)[:len(servingTestCall(`,"id":1`))-1]} {
		var out bytes.Buffer
		require.NoError(t, serveWithTestDispatch(context.Background(), input, &out, nil))
		require.Empty(t, out.String(), "native reader drops unterminated EOF lines")
	}
	var out bytes.Buffer
	fake := &boundedNativeDispatch{}
	err := serveWithTestDispatch(context.Background(), "{invalid}\n"+servingTestCall(`,"id":1`), &out, fake.dispatch)
	require.ErrorContains(t, err, "read request")
	require.Zero(t, fake.calls)
	require.Empty(t, out.String())
}

func TestServeStdioWithDispatchNativeIOErrors(t *testing.T) {
	want := errors.New("native I/O failed")
	reader := servingReaderFunc(func([]byte) (int, error) { return 0, want })
	var out bytes.Buffer
	require.ErrorIs(t, ServeStdioWithDispatch(context.Background(), servingTestConfig(), reader, &out, nil, nil), want)
	require.Empty(t, out.String())

	writer := servingWriterFunc(func([]byte) (int, error) { return 0, want })
	input := strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1}` + "\n")
	require.ErrorIs(t, ServeStdioWithDispatch(context.Background(), servingTestConfig(), input, writer, nil, nil), want)
}

func TestEligibleNativeIDInvalidInput(t *testing.T) {
	for _, id := range []string{"", "invalid", "1 2", "[", "true"} {
		require.False(t, eligibleNativeID(json.RawMessage(id)), id)
	}
}

func TestServeStdioWithDispatchNativeIDEligibility(t *testing.T) {
	for _, id := range []string{`"string"`, `0`, `1.5`, `null`, `true`, `{}`, `[]`} {
		t.Run(id, func(t *testing.T) {
			var calls int
			dispatch := func(context.Context, *jsonrpc.Request, func(*jsonrpc.Response) error) (bool, error) {
				calls++
				return false, nil
			}
			var out bytes.Buffer
			require.NoError(t, serveWithTestDispatch(context.Background(), servingTestCall(`,"id":`+id), &out, dispatch))
			if id == "true" || id == "{}" || id == "[]" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			require.Contains(t, out.String(), "LEGACY FALLTHROUGH", "unhandled native requests retain the legacy policy")
		})
	}
}
