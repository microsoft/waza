package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestRunnerOperationActualProtectedSubmissionAndBodyLimit(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	service := runnerunit.NewService(nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, inputErr := ReadRunnerOperationInput(w, r, runnerunit.MaxSubmissionBytes)
		if inputErr != nil {
			body, err := json.Marshal(map[string]string{"code": inputErr.Code})
			require.NoError(t, err)
			require.NoError(t, WriteRunnerOperationJSON(w, inputErr.Status, 6, body, false))
			return
		}
		result, err := service.Submit(r.Context(), data, backend, hooks)
		require.NoError(t, err)
		body, err := runnerunit.EncodeSubmissionResult(result)
		require.NoError(t, err)
		require.NoError(t, WriteRunnerOperationJSON(w, http.StatusOK, result.ExitCode, body, true))
	}))
	defer server.Close()
	for _, control := range []string{"exact-limit", "over-limit", "wrong-host", "wrong-origin", "wrong-media"} {
		t.Run(control, func(t *testing.T) {
			data := append(bytes.Clone(wrapper), bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes-len(wrapper))...)
			require.Len(t, data, runnerunit.MaxSubmissionBytes)
			if control == "over-limit" {
				data = append(data, ' ')
			}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewReader(data))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			switch control {
			case "wrong-host":
				request.Host = "evil.example"
			case "wrong-origin":
				request.Header.Set("Origin", "https://evil.example")
			case "wrong-media":
				request.Header.Set("Content-Type", "text/plain")
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, 1, bytes.Count(body, []byte{'\n'}))
			switch control {
			case "exact-limit":
				require.Equal(t, http.StatusOK, response.StatusCode)
				result, err := runnerunit.ParseSubmissionResult(body)
				require.NoError(t, err)
				require.NotNil(t, result.Completion)
				require.Empty(t, result.Report)
				require.Equal(t, "0", response.Header.Get("X-Waza-Exit-Code"))
			case "over-limit":
				require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
				require.JSONEq(t, `{"code":"request_too_large"}`, string(body))
				require.True(t, response.Close)
			case "wrong-media":
				require.Equal(t, http.StatusUnsupportedMediaType, response.StatusCode)
			default:
				require.Equal(t, http.StatusForbidden, response.StatusCode)
			}
		})
	}
}

type operationWriter struct {
	*httptest.ResponseRecorder
	writes           int
	readErr          error
	writeDeadlineErr error
	writeErr         error
	short            bool
	flushErr         error
}

func (w *operationWriter) SetReadDeadline(time.Time) error  { return w.readErr }
func (w *operationWriter) SetWriteDeadline(time.Time) error { return w.writeDeadlineErr }
func (w *operationWriter) FlushError() error                { return w.flushErr }
func (w *operationWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return len(data) - 1, nil
	}
	return w.ResponseRecorder.Write(data)
}

func TestRunnerOperationEmissionAndAdmissionFailures(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\n\n", "\r\n \t\n"} {
		w := &operationWriter{ResponseRecorder: httptest.NewRecorder()}
		require.NoError(t, WriteRunnerOperationJSON(w, http.StatusOK, 0, []byte("{\"recorded\":0}"+suffix), true))
		require.Equal(t, "{\"recorded\":0}\n", w.Body.String())
		require.Equal(t, 1, w.writes)
	}
	for _, control := range []string{"good", "invalid-json", "write", "short", "flush", "write-deadline"} {
		t.Run(control, func(t *testing.T) {
			w := &operationWriter{ResponseRecorder: httptest.NewRecorder()}
			body := []byte(`{"recorded":0}`)
			switch control {
			case "invalid-json":
				body = []byte("{")
			case "write":
				w.writeErr = io.ErrClosedPipe
			case "short":
				w.short = true
			case "flush":
				w.flushErr = io.ErrClosedPipe
			case "write-deadline":
				w.writeDeadlineErr = http.ErrNotSupported
				require.PanicsWithValue(t, http.ErrAbortHandler, func() {
					_ = WriteRunnerOperationJSON(w, http.StatusOK, 0, body, true)
				})
				require.Zero(t, w.writes)
				return
			}
			err := WriteRunnerOperationJSON(w, http.StatusOK, 0, body, true)
			if control == "good" {
				require.NoError(t, err)
				require.Equal(t, append(body, '\n'), w.Body.Bytes())
			} else {
				require.Error(t, err)
			}
			if control == "invalid-json" {
				require.Zero(t, w.writes)
			} else {
				require.Equal(t, 1, w.writes, "delivery failure must not retry")
			}
		})
	}
	for _, control := range []string{"invalid-limit", "deadline", "remote", "canceled"} {
		t.Run(control, func(t *testing.T) {
			w := &operationWriter{ResponseRecorder: httptest.NewRecorder()}
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/operation", strings.NewReader("{}"))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Content-Type", "application/json")
			limit := int64(32)
			switch control {
			case "invalid-limit":
				limit = 0
			case "deadline":
				w.readErr = http.ErrNotSupported
			case "remote":
				r.RemoteAddr = "192.0.2.1:1234"
			case "canceled":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			data, err := ReadRunnerOperationInput(w, r, limit)
			require.Error(t, err)
			require.Nil(t, data)
			if control == "canceled" {
				require.True(t, errors.Is(err, context.Canceled))
			}
		})
	}
}
