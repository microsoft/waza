package webapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestRunnerTerminalPartialHTTPUploadTimesOutOnce(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck
	require.NoError(t, conn.SetDeadline(time.Now().Add(8*time.Second)))
	_, err = fmt.Fprint(conn, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\nConnection: close\r\n\r\n{")
	require.NoError(t, err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestTimeout, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	result, err := runnerterminal.Validate(body, nil)
	require.NoError(t, err, "%s", body)
	require.Equal(t, runnerterminal.Timeout, result.Outcome)
	require.Equal(t, 4, result.ExitCode)
	require.Equal(t, "input", result.FailureBoundary)
	require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
}

func TestRunnerTerminalPartialWrongMediaDoesNotDrain(t *testing.T) {
	mux := http.NewServeMux()
	var factories atomic.Int32
	RegisterRunnerRoutesWithService(mux, runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		factories.Add(1)
		return nil, errors.New("unadmitted input must not construct a runtime")
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, media := range []string{"", "text/plain", "application/json; charset"} {
		t.Run(media, func(t *testing.T) {
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			require.NoError(t, err)
			defer conn.Close() //nolint:errcheck
			require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
			header := ""
			if media != "" {
				header = "Content-Type: " + media + "\r\n"
			}
			_, err = fmt.Fprintf(conn, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\n%sContent-Length: 100\r\n\r\n{", header)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			require.NoError(t, err, "rejection must not wait for the remaining request body")
			require.Equal(t, http.StatusUnsupportedMediaType, response.StatusCode)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			envelope, err := runnerterminal.Validate(body, nil)
			require.NoError(t, err)
			require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
			require.Equal(t, "6", response.Header.Get("X-Waza-Exit-Code"))
			require.True(t, response.Close)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Identity)
			require.Nil(t, envelope.Selection)
			require.Nil(t, envelope.Observation)
			require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
			require.Zero(t, factories.Load())
		})
	}
}

func TestRunnerTerminalAdmittedFailureKeepsConnection(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.Equal(t, "ready", admitted.Status)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
		GraderIDs: []string{"_output_contains"}}
	for _, tc := range []struct {
		cause   error
		outcome runnerterminal.Outcome
		exit    int
	}{
		{errors.New("injected startup failure"), runnerterminal.InfrastructureFailure, 2},
		{context.Canceled, runnerterminal.Canceled, 3},
		{context.DeadlineExceeded, runnerterminal.Timeout, 4},
	} {
		t.Run(tc.cause.Error(), func(t *testing.T) {
			var factories atomic.Int32
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				factories.Add(1)
				return nil, tc.cause
			})
			mux := http.NewServeMux()
			RegisterRunnerRoutesWithService(mux, service)
			server := httptest.NewServer(mux)
			defer server.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			require.NoError(t, err)
			defer conn.Close() //nolint:errcheck
			require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
			_, err = fmt.Fprintf(conn, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(request), request)
			require.NoError(t, err)
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.False(t, response.Close)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			envelope, err := runnerterminal.Validate(body, binding)
			require.NoError(t, err)
			require.Equal(t, tc.outcome, envelope.Outcome)
			require.Equal(t, tc.exit, envelope.ExitCode)
			require.Equal(t, "startup", envelope.FailureBoundary)
			require.Equal(t, binding.Identity, envelope.Identity)
			require.Equal(t, binding.Selection, *envelope.Selection)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Observation)
			require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
			require.Equal(t, int32(1), factories.Load())
			_, err = fmt.Fprint(conn, "GET /api/v1/runner/capabilities HTTP/1.1\r\nHost: localhost\r\n\r\n")
			require.NoError(t, err)
			response, err = http.ReadResponse(reader, nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			var advertisement runnercontract.Advertisement
			require.NoError(t, json.NewDecoder(response.Body).Decode(&advertisement))
			require.NoError(t, response.Body.Close())
			require.Equal(t, runnercontract.Capabilities(), advertisement)
		})
	}
}

type terminalDeadlineFailure struct {
	http.ResponseWriter
	writes int
}

func (w *terminalDeadlineFailure) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *terminalDeadlineFailure) SetWriteDeadline(deadline time.Time) error {
	w.writes++
	if w.writes == 2 {
		return errors.New("injected late output deadline failure")
	}
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}

func TestRunnerTerminalLateOutputFailureRetainsObservation(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	admitted := runnerunit.Validate(request)
	require.NotNil(t, admitted.Identity)
	binding := &runnerterminal.Binding{Selection: *admitted.Resolved, Identity: admitted.Identity,
		GraderIDs: []string{"_output_contains"}}
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(&terminalDeadlineFailure{ResponseWriter: w}, r)
	}))
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(request))
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusInternalServerError, response.StatusCode)
	require.Equal(t, "2", response.Header.Get("X-Waza-Exit-Code"))
	envelope, err := runnerterminal.Validate(body, binding)
	require.NoError(t, err, "%s", body)
	require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
	require.Equal(t, "output", envelope.FailureBoundary)
	require.Equal(t, binding.Identity, envelope.Identity)
	require.Equal(t, "passed", envelope.Observation.Status)
	require.Equal(t, 1, envelope.EngineCalls)
	require.Contains(t, envelope.Diagnostics[0].Message, "after outcome success")
	require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
}

type terminalTransportFailure struct {
	http.ResponseWriter
	control string
	attempt []byte
	calls   int
	flushes int
}

func (w *terminalTransportFailure) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *terminalTransportFailure) Write(data []byte) (int, error) {
	w.calls++
	w.attempt = bytes.Clone(data)
	if w.control == "flush" {
		return w.ResponseWriter.Write(data)
	}
	n, err := w.ResponseWriter.Write(data[:len(data)/2])
	if err != nil {
		return n, err
	}
	if w.control == "write" {
		return n, errors.New("injected partial HTTP write failure")
	}
	return n, nil
}

func (w *terminalTransportFailure) FlushError() error {
	w.flushes++
	return errors.New("injected HTTP flush failure")
}

func TestRunnerTerminalHTTPOutputFailuresNeverRetry(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, control := range []string{"short", "write", "flush"} {
		t.Run(control, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterRunnerRoutes(mux)
			finished := make(chan *terminalTransportFailure, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				output := &terminalTransportFailure{ResponseWriter: w, control: control}
				mux.ServeHTTP(output, r)
				finished <- output
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			response, err := client.Post(server.URL+"/api/v1/runner/run", "application/json", bytes.NewReader(request))
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			output := <-finished
			require.Equal(t, 1, output.calls)
			require.True(t, json.Valid(output.attempt), "the actual producer must prepare a complete envelope")
			require.Equal(t, "0", response.Header.Get("X-Waza-Exit-Code"), "delivery failure cannot retract an already-sent decision")
			if control == "flush" {
				require.Equal(t, 1, output.flushes)
				require.Equal(t, output.attempt, body)
			} else {
				require.Zero(t, output.flushes)
				require.Equal(t, output.attempt[:len(output.attempt)/2], body)
				require.False(t, json.Valid(body), "partial delivery must not become a valid fallback")
			}
		})
	}
}

func TestRunnerTerminalHTTPMediaAndSizeFailures(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, tc := range []struct {
		body, media string
		status      int
	}{
		{"{}", "text/plain", http.StatusUnsupportedMediaType},
		{strings.Repeat(" ", (4<<20)+1), "application/json", http.StatusRequestEntityTooLarge},
	} {
		response, err := server.Client().Post(server.URL+"/api/v1/runner/run", tc.media, strings.NewReader(tc.body))
		require.NoError(t, err)
		require.Equal(t, tc.status, response.StatusCode)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		envelope, err := runnerterminal.Validate(body, nil)
		require.NoError(t, err)
		require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome)
		require.Equal(t, "6", response.Header.Get("X-Waza-Exit-Code"))
	}
}
