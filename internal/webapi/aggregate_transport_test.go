package webapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/stretchr/testify/require"
)

func TestAggregateHTTPStalledBodyIsBounded(t *testing.T) {
	var opened atomic.Int32
	mux := http.NewServeMux()
	RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		opened.Add(1)
		return manifestaggregate.Sources{}, nil, errors.New("private provider error")
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
	require.NoError(t, err)
	defer func() { require.NoError(t, connection.Close()) }()
	require.NoError(t, connection.SetDeadline(time.Now().Add(8*time.Second)))
	_, err = fmt.Fprint(connection, "POST /api/v1/aggregates HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\n\r\n[")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
	require.NoError(t, err, "stalled input must produce a bounded explicit HTTP failure")
	defer func() { require.NoError(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestTimeout, response.StatusCode)
	require.JSONEq(t, `{"error":"aggregate_request_timeout"}`, string(body))
	require.True(t, response.Close, "partial input must not trigger an unbounded drain")
	require.Zero(t, opened.Load())
}

func TestAggregateHTTPRejectedPartialInputDoesNotDrain(t *testing.T) {
	for _, control := range []struct {
		name   string
		cancel bool
	}{
		{name: "size limit with withheld final byte"},
		{name: "request cancellation", cancel: true},
	} {
		t.Run(control.name, func(t *testing.T) {
			var opened atomic.Int32
			mux := http.NewServeMux()
			RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
				opened.Add(1)
				return manifestaggregate.Sources{}, nil, errors.New("private provider error")
			})
			cancels := make(chan context.CancelFunc, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				cancels <- cancel
				mux.ServeHTTP(w, r.WithContext(ctx))
			}))
			defer server.Close()
			connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
			require.NoError(t, err)
			defer func() { require.NoError(t, connection.Close()) }()
			require.NoError(t, connection.SetDeadline(time.Now().Add(8*time.Second)))
			payload := strings.Repeat(" ", artifactmanifest.MaxManifestBytes+1)
			status, code := http.StatusBadRequest, "invalid_aggregate_request"
			if control.cancel {
				payload, status, code = "[", http.StatusRequestTimeout, "aggregate_request_timeout"
			}
			_, err = fmt.Fprintf(connection, "POST /api/v1/aggregates HTTP/1.1\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s", len(payload)+1, payload)
			require.NoError(t, err)
			select {
			case cancel := <-cancels:
				if control.cancel {
					cancel()
				}
			case <-time.After(8 * time.Second):
				t.Fatal("request did not reach the registered handler")
			}
			require.NoError(t, connection.SetReadDeadline(time.Now().Add(2*time.Second)))
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
			require.NoError(t, err, "rejected partial input must not wait for the missing byte or five-second read deadline")
			defer func() { require.NoError(t, response.Body.Close()) }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, status, response.StatusCode)
			require.JSONEq(t, `{"error":"`+code+`"}`, string(body))
			require.True(t, response.Close)
			require.Zero(t, opened.Load())
		})
	}
}

type aggregateDeadlineFault struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
	readFailure    int
	writeFailure   int
	writeAlways    bool
	shortWrite     bool
	writeError     bool
	flushError     bool
	headers        int
}

func (w *aggregateDeadlineFault) SetReadDeadline(deadline time.Time) error {
	w.readDeadlines = append(w.readDeadlines, deadline)
	if len(w.readDeadlines) == w.readFailure {
		return errors.New("private read deadline failure")
	}
	return nil
}

func (w *aggregateDeadlineFault) SetWriteDeadline(deadline time.Time) error {
	w.writeDeadlines = append(w.writeDeadlines, deadline)
	if w.writeAlways || len(w.writeDeadlines) == w.writeFailure {
		return http.ErrNotSupported
	}
	return nil
}

func (w *aggregateDeadlineFault) WriteHeader(status int) {
	w.headers++
	w.ResponseRecorder.WriteHeader(status)
}

func (w *aggregateDeadlineFault) Write(data []byte) (int, error) {
	if w.writeError {
		return 0, errors.New("private write failure")
	}
	if w.shortWrite {
		return w.ResponseRecorder.Write(data[:len(data)-1])
	}
	return w.ResponseRecorder.Write(data)
}

func (w *aggregateDeadlineFault) FlushError() error {
	if w.flushError {
		return errors.New("private flush failure")
	}
	w.Flush()
	return nil
}

type aggregateReadCounter struct {
	io.Reader
	calls int
}

func (r *aggregateReadCounter) Read(data []byte) (int, error) {
	r.calls++
	return r.Reader.Read(data)
}

func TestAggregateHTTPDeadlineAdmissionFailures(t *testing.T) {
	for _, control := range []struct {
		name         string
		readFailure  int
		writeFailure int
		readBody     bool
	}{
		{name: "read setup", readFailure: 1},
		{name: "write support probe", writeFailure: 1},
		{name: "read reset", readFailure: 2, readBody: true},
	} {
		t.Run(control.name, func(t *testing.T) {
			opened := 0
			mux := http.NewServeMux()
			RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
				opened++
				return manifestaggregate.Sources{}, nil, errors.New("private provider error")
			})
			body := &aggregateReadCounter{Reader: strings.NewReader(`[{"manifest_sha256":"` + strings.Repeat("a", 64) + `"}]`)}
			writer := &aggregateDeadlineFault{ResponseRecorder: httptest.NewRecorder(),
				readFailure: control.readFailure, writeFailure: control.writeFailure}
			mux.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", body))
			require.Equal(t, http.StatusInternalServerError, writer.Code)
			require.JSONEq(t, `{"error":"aggregate_transport_unavailable"}`, writer.Body.String())
			require.Equal(t, "close", writer.Header().Get("Connection"))
			require.NotContains(t, writer.Body.String(), "private")
			require.Equal(t, control.readBody, body.calls > 0)
			require.Zero(t, opened)
			require.False(t, writer.readDeadlines[0].IsZero())
			if control.readBody {
				require.Len(t, writer.readDeadlines, 2)
				require.True(t, writer.readDeadlines[1].IsZero(), "reset failure must not admit storage")
			}
			require.False(t, writer.writeDeadlines[len(writer.writeDeadlines)-1].IsZero())
		})
	}
}

func TestAggregateHTTPDeliveryFailuresAbort(t *testing.T) {
	for _, control := range []struct {
		name        string
		writeAlways bool
		shortWrite  bool
		writeError  bool
		flushError  bool
	}{
		{name: "unsupported output", writeAlways: true},
		{name: "short write", shortWrite: true},
		{name: "write error", writeError: true},
		{name: "flush error", flushError: true},
	} {
		t.Run(control.name, func(t *testing.T) {
			opened := 0
			mux := http.NewServeMux()
			RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
				opened++
				return manifestaggregate.Sources{}, nil, errors.New("private provider error")
			})
			writer := &aggregateDeadlineFault{ResponseRecorder: httptest.NewRecorder(),
				writeAlways: control.writeAlways, shortWrite: control.shortWrite,
				writeError: control.writeError, flushError: control.flushError}
			require.PanicsWithValue(t, http.ErrAbortHandler, func() {
				mux.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(`[]`)))
			})
			require.Zero(t, opened)
			if control.writeAlways {
				require.Zero(t, writer.headers, "unsupported delivery must not commit an implicit 200")
				require.Empty(t, writer.Body.Bytes())
			} else {
				require.Equal(t, http.StatusBadRequest, writer.Code)
			}
		})
	}
}

type aggregateUnsupportedOutput struct{ http.ResponseWriter }

func (w aggregateUnsupportedOutput) Unwrap() http.ResponseWriter    { return w.ResponseWriter }
func (aggregateUnsupportedOutput) SetWriteDeadline(time.Time) error { return http.ErrNotSupported }

func TestAggregateHTTPUnsupportedDeliveryHasNoSuccessResponse(t *testing.T) {
	var opened atomic.Int32
	mux := http.NewServeMux()
	RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		opened.Add(1)
		return manifestaggregate.Sources{}, nil, errors.New("private provider error")
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(aggregateUnsupportedOutput{w}, r)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/api/v1/aggregates", "application/json",
		strings.NewReader(`[{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}]`))
	require.Error(t, err, "unsupported delivery must abort instead of returning an implicit 200")
	require.Nil(t, response)
	require.Zero(t, opened.Load())
}
