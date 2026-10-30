package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestRunnerServerCancellationWaitsForRuntimeConstruction(t *testing.T) {
	srv, err := New(Config{Port: freePort(t), ResultsDir: t.TempDir(), NoBrowser: true, Logger: discardLogger()})
	require.NoError(t, err)
	started, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mux := http.NewServeMux()
	webapi.RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		close(started)
		<-ctx.Done()
		close(cleaning)
		<-release
		return execution.NewMockEngine("mock"), nil
	}))
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.srv.Handler = mux
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	waitForHealthEndpoint(t, srv.cfg.Port)
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	response := make(chan runnerunit.Report, 1)
	clientErrors := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/api/v1/runner/run", srv.cfg.Port), "application/json", bytes.NewReader(data))
		if err != nil {
			clientErrors <- err
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		var report runnerunit.Report
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			clientErrors <- err
			return
		}
		response <- report
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime construction did not start")
	}
	cancel()
	select {
	case <-cleaning:
	case <-time.After(5 * time.Second):
		t.Fatal("server cancellation did not reach runtime construction")
	}
	select {
	case err := <-done:
		t.Fatalf("server exited before cleanup completed: %v", err)
	default:
	}
	close(release)
	select {
	case report := <-response:
		require.Equal(t, "execution_error", report.Status)
		require.Zero(t, report.Calls)
		require.Nil(t, report.Observation)
		require.Contains(t, report.Diagnostics[0].Message, "context canceled")
	case err := <-clientErrors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP attempt response was lost")
	}
	require.NoError(t, <-done)
}

func TestRunnerServerCancellationInterruptsPartialUpload(t *testing.T) {
	srv, err := New(Config{Port: freePort(t), ResultsDir: t.TempDir(), NoBrowser: true, Logger: discardLogger()})
	require.NoError(t, err)
	entered := make(chan struct{})
	handler := srv.srv.Handler
	srv.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runner/run" {
			close(entered)
		}
		handler.ServeHTTP(w, r)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	waitForHealthEndpoint(t, srv.cfg.Port)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", srv.cfg.Port))
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	require.NoError(t, conn.SetDeadline(time.Now().Add(4*time.Second)))
	_, err = fmt.Fprint(conn, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{")
	require.NoError(t, err)
	<-entered
	cancel()
	response, err := io.ReadAll(conn)
	require.NoError(t, err)
	require.Contains(t, string(response), "400 Bad Request")
	require.NotContains(t, string(response), `"status":"completed"`)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("partial upload delayed shutdown")
	}
}
