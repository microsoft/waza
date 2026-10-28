//go:build linux || darwin

package webapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

type signaledBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (body *signaledBody) Read(data []byte) (int, error) {
	body.once.Do(func() { close(body.started) })
	return body.ReadCloser.Read(data)
}

func TestRunnerCanceledPartialUploadIsPreAdmission(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	reader, writer := io.Pipe()
	defer writer.Close() //nolint:errcheck
	body := &signaledBody{ReadCloser: reader, started: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", body)
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(response, request)
		close(done)
	}()
	<-body.started
	_, err := writer.Write([]byte(`{"schema_version":`))
	require.NoError(t, err)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled body read was not interrupted")
	}
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.NotContains(t, response.Body.String(), "waza.runner-result")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "incomplete transport must not invent a runner invocation")
}

func TestRunnerSlowUploadHasBoundedReadDeadline(t *testing.T) {
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := readRunnerBody(w, r, 20*time.Millisecond)
		done <- err
	}))
	defer server.Close()
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer connection.Close() //nolint:errcheck
	_, err = fmt.Fprintf(connection, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n{")
	require.NoError(t, err)
	select {
	case err := <-done:
		require.Error(t, err, "a partial slow reader cannot hang the HTTP handler")
	case <-time.After(5 * time.Second):
		t.Fatal("upload deadline did not interrupt network body")
	}
}

func TestRunnerPartialDisconnectHasNoAdmittedEnvelope(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		close(done)
	}))
	defer server.Close()
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	_, err = fmt.Fprintf(connection, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")
	require.NoError(t, err)
	require.NoError(t, connection.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnected upload blocked handler")
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type admittedCancelEngine struct {
	execution.AgentEngine
	started chan struct{}
}

func (engine *admittedCancelEngine) Initialize(ctx context.Context) error {
	close(engine.started)
	<-ctx.Done()
	return ctx.Err()
}

func (*admittedCancelEngine) Shutdown(context.Context) error { return nil }

func TestRunnerAdmittedDisconnectPersistsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	service := runnercontract.NewService(runnercontract.WithEngineFactory(func(models.Config) (execution.AgentEngine, error) {
		return &admittedCancelEngine{started: started}, nil
	}))
	mux := http.NewServeMux()
	registerRunnerRoutes(mux, dir, service)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		close(done)
	}))
	defer server.Close()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer connection.Close() //nolint:errcheck
	_, err = fmt.Fprintf(connection, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(data), data)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached admission")
	}
	require.NoError(t, connection.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted disconnect did not terminalize")
	}
	results, err := readRunnerResults(dir)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, runnercontract.Canceled, results[0].Outcome)
	require.True(t, results[0].SelectionValid)
	require.NoError(t, runnercontract.ValidateResult(&results[0]))
}

type brokenRunnerResponse struct {
	header          http.Header
	headers, writes int
}

func (w *brokenRunnerResponse) Header() http.Header { return w.header }
func (w *brokenRunnerResponse) WriteHeader(int)     { w.headers++ }
func (w *brokenRunnerResponse) Write([]byte) (int, error) {
	w.writes++
	return 0, io.ErrClosedPipe
}

func TestRunnerResponseFailureDoesNotDuplicatePersistedTerminal(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runner/run", bytes.NewReader(data))
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	response := &brokenRunnerResponse{header: http.Header{}}
	mux.ServeHTTP(response, request)
	require.Equal(t, 1, response.headers)
	require.Equal(t, 1, response.writes, "a delivery failure must never emit a second terminal result")
	results, err := readRunnerResults(dir)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, runnercontract.ValidateResult(&results[0]))
}

func TestRunnerBodyReadIsBoundedAndReportsPartialError(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", runnercontract.MaxRequestBytes+100)))
	data, err := readRunnerBody(httptest.NewRecorder(), request, time.Second)
	require.NoError(t, err)
	require.Len(t, data, runnercontract.MaxRequestBytes+1)
	reader, writer := io.Pipe()
	request = httptest.NewRequest(http.MethodPost, "/", reader)
	go func() {
		_, _ = writer.Write([]byte("{"))
		_ = writer.CloseWithError(io.ErrUnexpectedEOF)
	}()
	_, err = readRunnerBody(httptest.NewRecorder(), request, time.Second)
	require.True(t, errors.Is(err, io.ErrUnexpectedEOF))
}
