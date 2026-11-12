package webapi

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/stretchr/testify/require"
)

type aggregateResetFailure struct{ http.ResponseWriter }

func (w aggregateResetFailure) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w aggregateResetFailure) SetReadDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		return errors.New("private reset cause")
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}

func TestAggregateHTTPBlockedDiagnosticsDoNotBlockDelivery(t *testing.T) {
	require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
	sink := &blockedRunnerDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	var diagnostics bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(&diagnostics, sink), nil)))
	var servers []*httptest.Server
	var handlers sync.WaitGroup
	t.Cleanup(func() {
		close(sink.release)
		for _, server := range servers {
			server.Close()
		}
		handlers.Wait()
		require.Eventually(t, func() bool { return !runnerDiagnosticActive.Load() }, 2*time.Second, time.Millisecond)
		slog.SetDefault(logger)
		require.Contains(t, diagnostics.String(), "private store cause", "operator diagnostics retain the actual cause")
	})
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	for index, control := range []struct {
		status  int
		code    string
		reset   bool
		store   bool
		invalid bool
	}{
		{503, "aggregate_store_unavailable", false, false, false},
		{500, "aggregate_transport_unavailable", true, false, false},
		{409, "", false, true, false},
		{503, "aggregate_evaluation_unavailable", false, false, true},
		{503, "aggregate_store_unavailable", false, false, false},
	} {
		mux := http.NewServeMux()
		RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
			if control.invalid {
				return manifestaggregate.Sources{}, func() error { return nil }, nil
			}
			if control.store {
				return manifestaggregate.Sources{Objects: backend}, func() error { return nil }, nil
			}
			return manifestaggregate.Sources{}, nil, errors.New("private store cause")
		})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlers.Add(1)
			defer handlers.Done()
			if control.reset {
				w = aggregateResetFailure{w}
			}
			mux.ServeHTTP(w, r)
		}))
		servers = append(servers, server)
		server.Client().Timeout = time.Second
		response, err := server.Client().Post(server.URL+"/api/v1/aggregates", "application/json",
			strings.NewReader(`[{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}]`))
		require.NoError(t, err, "diagnostic sink must not block HTTP delivery")
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		require.NoError(t, readErr)
		require.NoError(t, closeErr)
		require.Equal(t, control.status, response.StatusCode)
		require.NotContains(t, string(body), "private")
		if control.code != "" {
			require.JSONEq(t, `{"error":"`+control.code+`"}`, string(body))
		} else {
			report, err := manifestaggregate.Parse(body)
			require.NoError(t, err)
			require.Equal(t, "incomplete", report.State)
		}
		if index == 0 {
			select {
			case <-sink.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("operator diagnostic did not reach blocked sink")
			}
		}
	}
	require.EqualValues(t, 1, sink.writes.Load())
	require.GreaterOrEqual(t, runnerDiagnosticsSuppressed.Load(), uint64(4))
	stack := make([]byte, 1<<20)
	length := runtime.Stack(stack, true)
	require.Less(t, length, len(stack))
	require.Equal(t, 1, strings.Count(string(stack[:length]), "webapi.logRunnerError.func1("))
}
