package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

const ownedTestTimeout = 5 * time.Second

type ownedFakeState struct {
	mu                  sync.Mutex
	reserved, delivered int
}

func (state *ownedFakeState) counts() (int, int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.reserved, state.delivered
}

type ownedFakeAdmission struct {
	index int
	id    json.RawMessage
	ctx   context.Context
}

type ownedFakePrepare struct {
	state    *ownedFakeState
	gates    []<-chan struct{}
	admitted chan ownedFakeAdmission
	started  chan int
}

func newOwnedFakePrepare(state *ownedFakeState, gates ...<-chan struct{}) *ownedFakePrepare {
	return &ownedFakePrepare{
		state: state, gates: gates,
		admitted: make(chan ownedFakeAdmission, 16), started: make(chan int, 16),
	}
}

func (fake *ownedFakePrepare) prepare(ctx context.Context, req *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
	var params toolsCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, err
	}
	if params.Name != "owned" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fake.state.mu.Lock()
	index := fake.state.reserved
	if index >= len(fake.gates) {
		fake.state.mu.Unlock()
		return nil, errServingExhausted
	}
	fake.state.reserved++
	fake.state.mu.Unlock()
	id := bytes.Clone(req.ID)
	fake.admitted <- ownedFakeAdmission{index: index, id: id, ctx: ctx}
	return func(ctx context.Context, deliver func(*jsonrpc.Response) error) error {
		fake.started <- index
		if gate := fake.gates[index]; gate != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-gate:
			}
		}
		err := deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: id, Result: servingTestResult(fmt.Sprintf("step%d", index))})
		if err != nil {
			return err
		}
		fake.state.mu.Lock()
		fake.state.delivered++
		fake.state.mu.Unlock()
		return nil
	}, nil
}

type ownedObservedReader struct {
	*io.PipeReader
	entered chan struct{}
	eof     chan struct{}
	once    sync.Once
	eofOnce sync.Once
}

func (reader *ownedObservedReader) Read(data []byte) (int, error) {
	reader.once.Do(func() { close(reader.entered) })
	n, err := reader.PipeReader.Read(data)
	if err == io.EOF {
		reader.eofOnce.Do(func() { close(reader.eof) })
	}
	return n, err
}

type ownedObservedWriter struct {
	*io.PipeWriter
	entered chan struct{}
	once    sync.Once
}

func (writer *ownedObservedWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	return writer.PipeWriter.Write(data)
}

type ownedPipeSession struct {
	input        *io.PipeWriter
	output       *io.PipeReader
	readEntered  <-chan struct{}
	readEOF      <-chan struct{}
	writeEntered <-chan struct{}
	cancel       context.CancelFunc
	done         <-chan error
}

func startOwnedPipeSession(t *testing.T, prepare ToolCallPrepare, capacity int) *ownedPipeSession {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	reader := &ownedObservedReader{PipeReader: inReader, entered: make(chan struct{}), eof: make(chan struct{})}
	writer := &ownedObservedWriter{PipeWriter: outWriter, entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeOwnedStdioWithPrepare(ctx, servingTestConfig(), reader, writer, nil, prepare, capacity)
		close(done)
	}()
	session := &ownedPipeSession{
		input: inWriter, output: outReader, readEntered: reader.entered, readEOF: reader.eof,
		writeEntered: writer.entered, cancel: cancel, done: done,
	}
	t.Cleanup(func() {
		cancel()
		require.NoError(t, inWriter.Close())
		require.NoError(t, outReader.Close())
		select {
		case <-done:
		case <-time.After(ownedTestTimeout):
			t.Error("owned serving failed to join its reader and workers")
		}
	})
	return session
}

func writeOwnedInput(t *testing.T, session *ownedPipeSession, wire string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(session.input, wire)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(ownedTestTimeout):
		session.cancel()
		require.NoError(t, session.input.Close())
		<-done
		t.Fatal("native input reader blocked on response output")
	}
}

func awaitOwnedAdmission(t *testing.T, fake *ownedFakePrepare) ownedFakeAdmission {
	t.Helper()
	select {
	case admission := <-fake.admitted:
		return admission
	case <-time.After(ownedTestTimeout):
		t.Fatal("native request was not prepared")
		return ownedFakeAdmission{}
	}
}

func awaitOwnedSignal[T any](t *testing.T, signal <-chan T) T {
	t.Helper()
	select {
	case result := <-signal:
		return result
	case <-time.After(ownedTestTimeout):
		t.Fatal("owned serving did not make progress")
		var zero T
		return zero
	}
}

func awaitOwnedResult(t *testing.T, session *ownedPipeSession) error {
	t.Helper()
	return awaitOwnedSignal(t, session.done)
}

func readOwnedResponse(t *testing.T, session *ownedPipeSession, decoder *json.Decoder) jsonrpc.Response {
	t.Helper()
	type decoded struct {
		response jsonrpc.Response
		err      error
	}
	done := make(chan decoded, 1)
	go func() {
		var result decoded
		result.err = decoder.Decode(&result.response)
		done <- result
	}()
	select {
	case result := <-done:
		require.NoError(t, result.err)
		return result.response
	case <-time.After(ownedTestTimeout):
		session.cancel()
		require.NoError(t, session.output.Close())
		<-done
		t.Fatal("native response did not finish")
		return jsonrpc.Response{}
	}
}

func requireOwnedCounts(t *testing.T, state *ownedFakeState, reserved, delivered int) {
	t.Helper()
	actualReserved, actualDelivered := state.counts()
	require.Equal(t, reserved, actualReserved)
	require.Equal(t, delivered, actualDelivered)
}

func requireOwnedNoEmission(t *testing.T, session *ownedPipeSession) {
	t.Helper()
	data, err := io.ReadAll(session.output)
	require.NoError(t, err)
	require.Empty(t, data)
}

func ownedCancelNotification(requestID string) string {
	return `{"jsonrpc":"2.0","method":"` + ownedMCPCancelNotification + `","params":{"requestId":` + requestID + "}}\n"
}

func TestOwnedMCPNotificationCancellationConsumesReservation(t *testing.T) {
	for _, ids := range []struct{ request, cancel string }{
		{`7`, `7`}, {`null`, `null`}, {`"a"`, `"\u0061"`},
		{`9007199254740993123456789`, `9007199254740993123456789`},
	} {
		t.Run(ids.request, func(t *testing.T) {
			state := &ownedFakeState{}
			delay := make(chan struct{})
			fake := newOwnedFakePrepare(state, delay, nil)
			session := startOwnedPipeSession(t, fake.prepare, 2)
			writeOwnedInput(t, session, servingTestCall(`,"id":`+ids.request))
			admission := awaitOwnedAdmission(t, fake)
			require.Equal(t, 0, admission.index)
			require.Equal(t, 0, awaitOwnedSignal(t, fake.started))
			writeOwnedInput(t, session, ownedCancelNotification(ids.cancel))
			require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
			require.ErrorIs(t, admission.ctx.Err(), context.Canceled)
			requireOwnedNoEmission(t, session)
			requireOwnedCounts(t, state, 1, 0)

			// Reconstruct the private fake over the same state: cancellation
			// must not make the irrevocably reserved step available again.
			reconstructed := newOwnedFakePrepare(state, nil, nil)
			next := startOwnedPipeSession(t, reconstructed.prepare, 1)
			writeOwnedInput(t, next, servingTestCall(`,"id":9`))
			require.Equal(t, 1, awaitOwnedAdmission(t, reconstructed).index)
			require.NoError(t, next.input.Close())
			data, err := io.ReadAll(next.output)
			require.NoError(t, err)
			require.Contains(t, string(data), "step1")
			require.NoError(t, awaitOwnedResult(t, next))
			requireOwnedCounts(t, state, 2, 1)
		})
	}
}

func TestOwnedMCPRootCancellationInterruptsOwnedIO(t *testing.T) {
	t.Run("blocked reader", func(t *testing.T) {
		session := startOwnedPipeSession(t, nil, 1)
		awaitOwnedSignal(t, session.readEntered)
		session.cancel()
		require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
		requireOwnedNoEmission(t, session)
	})
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked writer and reader", true: "EOF draining blocked writer"}[eof], func(t *testing.T) {
			state := &ownedFakeState{}
			fake := newOwnedFakePrepare(state, nil)
			session := startOwnedPipeSession(t, fake.prepare, 1)
			writeOwnedInput(t, session, servingTestCall(`,"id":1`))
			awaitOwnedAdmission(t, fake)
			awaitOwnedSignal(t, session.writeEntered)
			if eof {
				require.NoError(t, session.input.Close())
				awaitOwnedSignal(t, session.readEOF)
			}
			session.cancel()
			require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
			requireOwnedNoEmission(t, session)
			requireOwnedCounts(t, state, 1, 0)
		})
	}
}

func TestOwnedMCPNotificationCancellationInterruptsBlockedAndQueuedWrites(t *testing.T) {
	state := &ownedFakeState{}
	fake := newOwnedFakePrepare(state, nil, nil)
	session := startOwnedPipeSession(t, fake.prepare, 2)
	writeOwnedInput(t, session, servingTestCall(`,"id":1`))
	awaitOwnedAdmission(t, fake)
	awaitOwnedSignal(t, session.writeEntered)
	writeOwnedInput(t, session, servingTestCall(`,"id":2`))
	awaitOwnedAdmission(t, fake)
	awaitOwnedSignal(t, fake.started)
	awaitOwnedSignal(t, fake.started)
	writeOwnedInput(t, session, ownedCancelNotification(`2`))
	require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
	requireOwnedNoEmission(t, session)
	requireOwnedCounts(t, state, 2, 0)
}

func TestOwnedMCPRootCancellationSupervisesCooperativePrepare(t *testing.T) {
	preparing := make(chan struct{})
	prepare := func(ctx context.Context, _ *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
		close(preparing)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	session := startOwnedPipeSession(t, prepare, 1)
	writeOwnedInput(t, session, servingTestCall(`,"id":1`))
	awaitOwnedSignal(t, preparing)
	session.cancel()
	require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
	requireOwnedNoEmission(t, session)
}

func TestOwnedMCPEOFDrainsDelayedAcceptedCallsInDeliveryOrder(t *testing.T) {
	state := &ownedFakeState{}
	first, second := make(chan struct{}), make(chan struct{})
	fake := newOwnedFakePrepare(state, first, second)
	session := startOwnedPipeSession(t, fake.prepare, 2)
	writeOwnedInput(t, session, servingTestCall(`,"id":1`)+servingTestCall(`,"id":2`))
	for index := range 2 {
		admission := awaitOwnedAdmission(t, fake)
		require.Equal(t, index, admission.index, "prepare reserves in serial native read order")
		require.Equal(t, fmt.Sprint(index+1), string(admission.id))
	}
	awaitOwnedSignal(t, fake.started)
	awaitOwnedSignal(t, fake.started)
	require.NoError(t, session.input.Close())
	awaitOwnedSignal(t, session.readEOF)
	decoder := json.NewDecoder(session.output)
	close(second)
	require.Equal(t, json.RawMessage(`2`), readOwnedResponse(t, session, decoder).ID)
	close(first)
	require.Equal(t, json.RawMessage(`1`), readOwnedResponse(t, session, decoder).ID)
	require.NoError(t, awaitOwnedResult(t, session))
	requireOwnedNoEmission(t, session)
	requireOwnedCounts(t, state, 2, 2)
}

func TestOwnedMCPNoIDNeverPreparesButExplicitNullDoes(t *testing.T) {
	state := &ownedFakeState{}
	fake := newOwnedFakePrepare(state, nil)
	session := startOwnedPipeSession(t, fake.prepare, 1)
	writeOwnedInput(t, session, servingTestCall("")+servingTestCall(`,"id":null`))
	admission := awaitOwnedAdmission(t, fake)
	require.Equal(t, json.RawMessage(`null`), admission.id)
	require.Equal(t, 0, admission.index)
	require.NoError(t, session.input.Close())
	response := readOwnedResponse(t, session, json.NewDecoder(session.output))
	require.Equal(t, json.RawMessage(`null`), response.ID)
	require.NoError(t, awaitOwnedResult(t, session))
	requireOwnedNoEmission(t, session)
	requireOwnedCounts(t, state, 1, 1)
}

func TestOwnedMCPDuplicateAndCapacityFailBeforePrepare(t *testing.T) {
	for _, test := range []struct {
		name, first, second, diagnostic string
		capacity                        int
	}{
		{"duplicate number", "1", "1", "duplicate active", 2},
		{"duplicate null", "null", "null", "duplicate active", 2},
		{"decoded string duplicate", `"a"`, `"\u0061"`, "duplicate active", 2},
		{"capacity", "1", "2", "in-flight capacity", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &ownedFakeState{}
			delay := make(chan struct{})
			fake := newOwnedFakePrepare(state, delay, nil)
			session := startOwnedPipeSession(t, fake.prepare, test.capacity)
			writeOwnedInput(t, session, servingTestCall(`,"id":`+test.first))
			awaitOwnedAdmission(t, fake)
			awaitOwnedSignal(t, fake.started)
			writeOwnedInput(t, session, servingTestCall(`,"id":`+test.second))
			require.ErrorContains(t, awaitOwnedResult(t, session), test.diagnostic)
			requireOwnedNoEmission(t, session)
			requireOwnedCounts(t, state, 1, 0)
		})
	}
}

func TestOwnedMCPActiveIDRetainedUntilWorkerCompletion(t *testing.T) {
	state := &ownedFakeState{}
	delivered := make(chan struct{})
	prepare := func(_ context.Context, req *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
		state.mu.Lock()
		state.reserved++
		state.mu.Unlock()
		id := bytes.Clone(req.ID)
		return func(ctx context.Context, deliver func(*jsonrpc.Response) error) error {
			if err := deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: id, Result: "committed"}); err != nil {
				return err
			}
			state.mu.Lock()
			state.delivered++
			state.mu.Unlock()
			close(delivered)
			<-ctx.Done()
			return ctx.Err()
		}, nil
	}
	session := startOwnedPipeSession(t, prepare, 2)
	writeOwnedInput(t, session, servingTestCall(`,"id":1`))
	require.Equal(t, json.RawMessage(`1`), readOwnedResponse(t, session, json.NewDecoder(session.output)).ID)
	awaitOwnedSignal(t, delivered)
	writeOwnedInput(t, session, servingTestCall(`,"id":1`))
	require.ErrorContains(t, awaitOwnedResult(t, session), "duplicate active")
	requireOwnedCounts(t, state, 1, 1)
}

func TestOwnedMCPUnknownCancelTargetsDoNotCancelPendingNativeCall(t *testing.T) {
	for _, test := range []struct{ requestID, notification string }{
		{`null`, `{"jsonrpc":"2.0","method":"` + ownedMCPCancelNotification + `","params":{}}` + "\n"},
		{`"1"`, ownedCancelNotification(`1`)},
		{`1`, ownedCancelNotification(`"1"`)},
		{`1`, ownedCancelNotification(`1.0`)},
		{`1.0`, ownedCancelNotification(`1`)},
		{`1`, ownedCancelNotification(`9`)},
		{`1`, ownedCancelNotification(`true`)},
		{`1`, `{"jsonrpc":"2.0","method":"` + ownedMCPCancelNotification + `","params":"invalid"}` + "\n"},
	} {
		t.Run(test.requestID+"/"+test.notification, func(t *testing.T) {
			state := &ownedFakeState{}
			delay := make(chan struct{})
			fake := newOwnedFakePrepare(state, delay)
			session := startOwnedPipeSession(t, fake.prepare, 2)
			writeOwnedInput(t, session, servingTestCall(`,"id":`+test.requestID))
			admission := awaitOwnedAdmission(t, fake)
			awaitOwnedSignal(t, fake.started)
			// The legacy response proves the reader progressed beyond the
			// notification without canceling or waiting for the delayed call.
			writeOwnedInput(t, session, test.notification+`{"jsonrpc":"2.0","method":"ping","id":"marker"}`+"\n")
			decoder := json.NewDecoder(session.output)
			require.Equal(t, json.RawMessage(`"marker"`), readOwnedResponse(t, session, decoder).ID)
			require.NoError(t, admission.ctx.Err())
			require.NoError(t, session.input.Close())
			close(delay)
			require.Equal(t, json.RawMessage(test.requestID), readOwnedResponse(t, session, decoder).ID)
			require.NoError(t, awaitOwnedResult(t, session))
			requireOwnedCounts(t, state, 1, 1)
		})
	}
}

func TestOwnedMCPDistinctNativeIDTypesAndLexemesAdmitTogether(t *testing.T) {
	state := &ownedFakeState{}
	delay := make(chan struct{})
	fake := newOwnedFakePrepare(state, delay, delay, delay)
	session := startOwnedPipeSession(t, fake.prepare, 3)
	for _, id := range []string{`1`, `1.0`, `"1"`} {
		writeOwnedInput(t, session, servingTestCall(`,"id":`+id))
		require.Equal(t, json.RawMessage(id), awaitOwnedAdmission(t, fake).id)
	}
	require.NoError(t, session.input.Close())
	awaitOwnedSignal(t, session.readEOF)
	close(delay)
	decoder := json.NewDecoder(session.output)
	actual := make(map[string]bool)
	for range 3 {
		response := readOwnedResponse(t, session, decoder)
		actual[string(response.ID)] = true
	}
	require.Equal(t, map[string]bool{`1`: true, `1.0`: true, `"1"`: true}, actual)
	require.NoError(t, awaitOwnedResult(t, session))
	requireOwnedCounts(t, state, 3, 3)
}

func TestOwnedMCPReaderDoesNotWriteLegacyResponses(t *testing.T) {
	for _, method := range []string{
		`{"jsonrpc":"2.0","method":"initialize","id":"legacy"}` + "\n",
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"legacy"},"id":"legacy"}` + "\n",
	} {
		t.Run(method, func(t *testing.T) {
			state := &ownedFakeState{}
			fake := newOwnedFakePrepare(state, nil)
			session := startOwnedPipeSession(t, fake.prepare, 2)
			writeOwnedInput(t, session, method)
			awaitOwnedSignal(t, session.writeEntered)
			writeOwnedInput(t, session, servingTestCall(`,"id":"pending"`))
			awaitOwnedAdmission(t, fake)
			session.cancel()
			require.ErrorIs(t, awaitOwnedResult(t, session), context.Canceled)
			requireOwnedNoEmission(t, session)
			requireOwnedCounts(t, state, 1, 0)
		})
	}
}

type ownedBufferEndpoint struct {
	bytes.Buffer
	closeErr error
	closes   atomic.Int32
	write    func([]byte) (int, error)
}

func (endpoint *ownedBufferEndpoint) Write(data []byte) (int, error) {
	if endpoint.write != nil {
		return endpoint.write(data)
	}
	return endpoint.Buffer.Write(data)
}

func (endpoint *ownedBufferEndpoint) Close() error {
	endpoint.closes.Add(1)
	return endpoint.closeErr
}

func TestOwnedMCPArgumentValidationDoesNotTransferOwnership(t *testing.T) {
	tests := []string{"nil context", "nil config", "zero capacity", "negative capacity", "nil reader", "nil writer", "typed nil reader", "typed nil writer", "alias"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			reader, writer := &ownedBufferEndpoint{}, &ownedBufferEndpoint{}
			var r io.ReadCloser = reader
			var w io.WriteCloser = writer
			ctx, cfg, capacity := context.Background(), servingTestConfig(), 1
			switch name {
			case "nil context":
				ctx = nil
			case "nil config":
				cfg = nil
			case "zero capacity":
				capacity = 0
			case "negative capacity":
				capacity = -1
			case "nil reader":
				r = nil
			case "nil writer":
				w = nil
			case "typed nil reader":
				r = (*ownedBufferEndpoint)(nil)
			case "typed nil writer":
				w = (*ownedBufferEndpoint)(nil)
			case "alias":
				w = reader
			}
			require.Error(t, ServeOwnedStdioWithPrepare(ctx, cfg, r, w, nil, nil, capacity))
			require.Zero(t, reader.closes.Load())
			require.Zero(t, writer.closes.Load())
		})
	}
}

func TestOwnedMCPPreCanceledContextOwnsAndClosesValidatedEndpoints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	closeErr := errors.New("owned close failure")
	reader, writer := &ownedBufferEndpoint{closeErr: closeErr}, &ownedBufferEndpoint{}
	calls := 0
	prepare := func(context.Context, *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
		calls++
		return nil, nil
	}
	err := ServeOwnedStdioWithPrepare(ctx, servingTestConfig(), reader, writer, nil, prepare, 1)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, closeErr)
	require.EqualValues(t, 1, reader.closes.Load())
	require.EqualValues(t, 1, writer.closes.Load())
	require.Zero(t, calls)
}

func TestOwnedMCPNilPrepareRetainsLegacyWireBehavior(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","method":"initialize","id":null}` + "\n"
	reader := io.NopCloser(strings.NewReader(input))
	writer := &ownedBufferEndpoint{}
	require.NoError(t, ServeOwnedStdioWithPrepare(context.Background(), servingTestConfig(), reader, writer, nil, nil, 1))
	var legacy bytes.Buffer
	ServeStdio(context.Background(), servingTestConfig(), strings.NewReader(input), &legacy, nil)
	require.Equal(t, legacy.String(), writer.String())
	require.EqualValues(t, 1, writer.closes.Load())
}

func TestOwnedMCPNativeIDKeys(t *testing.T) {
	for _, test := range []struct {
		id, key string
		native  bool
	}{
		{"", "", false}, {"null", "null", true},
		{`"a"`, "string:a", true}, {`"\u0061"`, "string:a", true},
		{"1", "number:1", true}, {"1.0", "number:1.0", true},
		{"1e0", "number:1e0", true}, {"1E0", "number:1E0", true},
		{`"1"`, "string:1", true}, {"-0", "number:-0", true},
		{"9007199254740993123456789", "number:9007199254740993123456789", true},
		{"true", "", false}, {"{}", "", false}, {"[]", "", false}, {"not-json", "", false},
	} {
		t.Run(test.id, func(t *testing.T) {
			key, native := ownedNativeIDKey(json.RawMessage(test.id))
			require.Equal(t, test.native, native)
			require.Equal(t, test.key, key)
		})
	}
}

func TestOwnedMCPPrepareAndExecutionErrorsDoNotFallThrough(t *testing.T) {
	for _, phase := range []string{"prepare", "execute"} {
		t.Run(phase, func(t *testing.T) {
			want := errors.New("configured operational failure")
			prepare := func(context.Context, *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
				if phase == "prepare" {
					return nil, want
				}
				return func(context.Context, func(*jsonrpc.Response) error) error { return want }, nil
			}
			writer := &ownedBufferEndpoint{}
			reader := io.NopCloser(strings.NewReader(servingTestCall(`,"id":1`)))
			err := ServeOwnedStdioWithPrepare(context.Background(), servingTestConfig(), reader, writer, nil, prepare, 1)
			require.ErrorIs(t, err, want)
			require.Empty(t, writer.String())
		})
	}
}

func TestOwnedMCPExecutionDeliveryProtections(t *testing.T) {
	writerErr, executeErr := errors.New("native writer failure"), errors.New("execute failed")
	for _, behavior := range []string{"nil", "no result", "both outcomes", "wrong ID", "wrong version", "marshal", "no delivery", "twice", "swallowed write", "joined errors", "short write", "retained"} {
		t.Run(behavior, func(t *testing.T) {
			writer := &ownedBufferEndpoint{}
			writes := 0
			if behavior == "swallowed write" || behavior == "joined errors" || behavior == "short write" {
				writer.write = func([]byte) (int, error) {
					writes++
					if behavior == "short write" {
						return 0, nil
					}
					return 0, writerErr
				}
			}
			var retained func(*jsonrpc.Response) error
			prepare := func(_ context.Context, req *jsonrpc.Request) (func(context.Context, func(*jsonrpc.Response) error) error, error) {
				id := bytes.Clone(req.ID)
				return func(_ context.Context, deliver func(*jsonrpc.Response) error) error {
					resp := &jsonrpc.Response{JSONRPC: "2.0", ID: id, Result: "native"}
					switch behavior {
					case "nil":
						resp = nil
					case "no result":
						resp.Result = nil
					case "both outcomes":
						resp.Error = jsonrpc.ErrInternalError(nil)
					case "wrong ID":
						resp.ID = json.RawMessage(`2`)
					case "wrong version":
						resp.JSONRPC = "1.0"
					case "marshal":
						resp.Result = make(chan int)
					case "no delivery":
						return nil
					case "retained":
						retained = deliver
						return nil
					}
					deliveryErr := deliver(resp)
					switch behavior {
					case "twice":
						require.NoError(t, deliveryErr)
						return deliver(resp)
					case "swallowed write":
						require.ErrorIs(t, deliveryErr, writerErr)
						return nil
					case "joined errors":
						require.ErrorIs(t, deliveryErr, writerErr)
						return executeErr
					default:
						return deliveryErr
					}
				}, nil
			}
			reader := io.NopCloser(strings.NewReader(servingTestCall(`,"id":1`)))
			err := ServeOwnedStdioWithPrepare(context.Background(), servingTestConfig(), reader, writer, nil, prepare, 1)
			require.Error(t, err)
			require.NotContains(t, writer.String(), "LEGACY FALLTHROUGH")
			switch behavior {
			case "swallowed write", "joined errors":
				require.ErrorIs(t, err, writerErr)
				require.Equal(t, 1, writes)
				if behavior == "joined errors" {
					require.ErrorIs(t, err, executeErr)
				}
			case "short write":
				require.ErrorIs(t, err, io.ErrShortWrite)
				require.Equal(t, 1, writes)
			case "retained":
				require.ErrorContains(t, retained(&jsonrpc.Response{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: "late"}), "after execution returned")
			}
		})
	}
}

func TestOwnedMCPMalformedReadFailsWithoutSyntheticPayload(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("{invalid}\n"))
	writer := &ownedBufferEndpoint{}
	require.ErrorContains(t, ServeOwnedStdioWithPrepare(context.Background(), servingTestConfig(), reader, writer, nil, nil, 1), "read request")
	require.Empty(t, writer.String())
	require.EqualValues(t, 1, writer.closes.Load())
}

func TestOwnedMCPUnterminatedEOFUsesNativeReadPolicy(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(strings.TrimSuffix(servingTestCall(`,"id":1`), "\n")))
	writer := &ownedBufferEndpoint{}
	state := &ownedFakeState{}
	fake := newOwnedFakePrepare(state, nil)
	require.NoError(t, ServeOwnedStdioWithPrepare(context.Background(), servingTestConfig(), reader, writer, nil, fake.prepare, 1))
	require.Empty(t, writer.String())
	requireOwnedCounts(t, state, 0, 0)
}
