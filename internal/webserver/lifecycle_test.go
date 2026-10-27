package webserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunnerServerCancellationWaitsForTerminalPublication(t *testing.T) {
	server, err := New(Config{ResultsDir: t.TempDir(), NoBrowser: true})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		_, _ = io.WriteString(w, "canceled terminal published")
	})
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx, func() error { return server.srv.Serve(listener) }) }()
	response := make(chan string, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		r, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			response <- err.Error()
			return
		}
		data, err := io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		if err != nil || closeErr != nil {
			response <- "response read/close failed"
			return
		}
		response <- string(data)
	}()
	<-entered
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("server cancellation did not reach active runner handler")
	}
	select {
	case err := <-done:
		t.Fatalf("server returned before terminal publication: %v", err)
	default:
	}
	close(release)
	require.Equal(t, "canceled terminal published", <-response)
	require.NoError(t, <-done)
}
