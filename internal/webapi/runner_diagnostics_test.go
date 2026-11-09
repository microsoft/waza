package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

type blockedRunnerDiagnosticSink struct {
	entered chan struct{}
	release chan struct{}
	writes  atomic.Int32
}

func (s *blockedRunnerDiagnosticSink) Write(data []byte) (int, error) {
	if s.writes.Add(1) == 1 {
		close(s.entered)
	}
	<-s.release
	return len(data), nil
}

type runnerDiagnosticRecorder struct {
	*httptest.ResponseRecorder
	bodyCalls   int
	flushCalls  int
	readCalls   int
	interrupted chan struct{}
}

func (w *runnerDiagnosticRecorder) Write(data []byte) (int, error) {
	w.bodyCalls++
	return w.ResponseRecorder.Write(data)
}

func (w *runnerDiagnosticRecorder) FlushError() error {
	w.flushCalls++
	w.Flush()
	return nil
}

func (w *runnerDiagnosticRecorder) SetWriteDeadline(time.Time) error { return nil }

func (w *runnerDiagnosticRecorder) SetReadDeadline(time.Time) error {
	w.readCalls++
	if w.interrupted != nil && w.readCalls == 2 {
		close(w.interrupted)
		return errors.New("injected cancellation deadline failure")
	}
	return nil
}

type runnerDiagnosticBlockedBody struct {
	entered     chan struct{}
	interrupted <-chan struct{}
}

func (b *runnerDiagnosticBlockedBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.interrupted
	return 0, context.Canceled
}

func (*runnerDiagnosticBlockedBody) Close() error { return nil }

func TestRunnerDiagnosticsBlockedSinkDoesNotBlockHandlersOrCallbacks(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.Equal(t, "ready", admitted.Status)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
		GraderIDs: []string{"_output_contains"}}
	var factories atomic.Int32
	mux := http.NewServeMux()
	RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return execution.NewMockEngine("mock"), nil
	}))

	sink := &blockedRunnerDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(logger)
	var release sync.Once
	var handlers []<-chan any
	defer func() {
		release.Do(func() { close(sink.release) })
		for _, done := range handlers {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("runner handler did not finish after releasing the diagnostic sink")
			}
		}
		require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	}()

	invoke := func(action string, output http.ResponseWriter, body io.ReadCloser) any {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		input := httptest.NewRequest(http.MethodPost, "/api/v1/runner/"+action, bytes.NewReader(request)).WithContext(ctx)
		input.Header.Set("Content-Type", "application/json")
		if body != nil {
			input.Body = body
		}
		done := make(chan any, 1)
		handlers = append(handlers, done)
		go func() {
			defer close(done)
			defer func() { done <- recover() }()
			mux.ServeHTTP(output, input)
		}()
		if blocked, ok := body.(*runnerDiagnosticBlockedBody); ok {
			select {
			case <-blocked.entered:
				cancel()
			case <-time.After(2 * time.Second):
				t.Fatal("runner handler never entered body admission")
			}
		}
		select {
		case result := <-done:
			return result
		case <-time.After(2 * time.Second):
			t.Fatal("runner handler or cancellation callback waited for the diagnostic sink")
			return nil
		}
	}
	recorder := func() *runnerDiagnosticRecorder {
		return &runnerDiagnosticRecorder{ResponseRecorder: httptest.NewRecorder()}
	}
	deadline := &terminalDeadlineFailure{ResponseWriter: recorder()}
	require.Equal(t, http.ErrAbortHandler, invoke("run", deadline, nil))
	require.Equal(t, int32(1), factories.Load(), "exercise the admitted production execution path")
	require.Equal(t, 2, deadline.writes)
	require.Zero(t, deadline.headerCalls)
	require.Zero(t, deadline.bodyCalls)
	require.Zero(t, deadline.flushCalls)
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the production diagnostic never reached the blocked sink")
	}
	suppressedBefore := runnerDiagnosticsSuppressed.Load()

	for _, control := range []string{"short", "write", "flush"} {
		record := recorder()
		output := &terminalTransportFailure{ResponseWriter: record, control: control}
		require.Nil(t, invoke("run", output, nil), control)
		require.Equal(t, 1, output.calls, "no output retry for %s", control)
		envelope, err := runnerterminal.Validate(output.attempt, binding)
		require.NoError(t, err)
		require.Equal(t, runnerterminal.Success, envelope.Outcome)
		if control == "flush" {
			require.Equal(t, 1, output.flushes)
			require.Equal(t, output.attempt, record.Body.Bytes())
		} else {
			require.Zero(t, output.flushes)
			require.Equal(t, output.attempt[:len(output.attempt)/2], record.Body.Bytes())
			require.False(t, json.Valid(record.Body.Bytes()), "partial delivery must not become a fallback")
		}
	}
	for _, action := range []string{"run", "validate"} {
		output := recorder()
		output.interrupted = make(chan struct{})
		body := &runnerDiagnosticBlockedBody{entered: make(chan struct{}), interrupted: output.interrupted}
		before := factories.Load()
		require.Nil(t, invoke(action, output, body), action)
		require.Equal(t, before, factories.Load(), "canceled admission must not construct a runtime")
		require.Equal(t, 2, output.readCalls, "the cancellation callback attempted the interrupt")
		require.Equal(t, 1, output.bodyCalls, "the handler returns without output retry")
		if action == "run" {
			envelope, err := runnerterminal.Validate(output.Body.Bytes(), nil)
			require.NoError(t, err)
			require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
			require.Equal(t, 1, output.flushCalls)
		} else {
			require.Equal(t, http.StatusBadRequest, output.Code)
			require.Contains(t, output.Body.String(), "context canceled")
			require.Zero(t, output.flushCalls)
		}
	}
	healthy := recorder()
	require.Nil(t, invoke("run", healthy, nil))
	envelope, err := runnerterminal.Validate(healthy.Body.Bytes(), binding)
	require.NoError(t, err)
	require.Equal(t, runnerterminal.Success, envelope.Outcome)
	require.Equal(t, 1, healthy.bodyCalls)
	require.Equal(t, 1, healthy.flushCalls)
	require.Equal(t, int32(1), sink.writes.Load())
	require.Equal(t, suppressedBefore+5, runnerDiagnosticsSuppressed.Load())
	stack := make([]byte, 1<<20)
	n := runtime.Stack(stack, true)
	require.Less(t, n, len(stack))
	require.Equal(t, 1, strings.Count(string(stack[:n]), "webapi.logRunnerError.func1("),
		"repeated failures must not accumulate logging goroutines behind the blocked handler")

	release.Do(func() { close(sink.release) })
	require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	var recovered bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&recovered, nil)))
	require.Equal(t, http.ErrAbortHandler, invoke("run", &terminalDeadlineFailure{ResponseWriter: recorder()}, nil))
	require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	require.Contains(t, recovered.String(), "aborting transport without output")
	require.Contains(t, recovered.String(), "suppressed_diagnostics=5")
	require.Zero(t, runnerDiagnosticsSuppressed.Load())
}
