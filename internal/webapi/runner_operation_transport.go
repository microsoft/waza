package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

type RunnerOperationError struct {
	Status int
	Code   string
	Err    error
}

func (e *RunnerOperationError) Error() string { return e.Err.Error() }
func (e *RunnerOperationError) Unwrap() error { return e.Err }

// ReadRunnerOperationInput admits transport only; callers still apply the
// strict request codec and capture policy before accessing immutable storage.
func ReadRunnerOperationInput(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, *RunnerOperationError) {
	if maxBytes <= 0 {
		return nil, &RunnerOperationError{http.StatusInternalServerError, "input_limit", fmt.Errorf("runner operation input limit must be positive")}
	}
	if err := authorizeNativeRunnerHTTP(r); err != nil {
		return nil, &RunnerOperationError{http.StatusForbidden, "operation_http_denied", err}
	}
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(5 * time.Second)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return nil, &RunnerOperationError{http.StatusInternalServerError, "input_deadline", fmt.Errorf("setting runner read deadline: %w", err)}
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		return nil, &RunnerOperationError{http.StatusInternalServerError, "output_deadline", fmt.Errorf("probing runner write deadline: %w", err)}
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, &RunnerOperationError{http.StatusUnsupportedMediaType, "invalid_media_type", fmt.Errorf("runner operation requires Content-Type: application/json")}
	}
	return readRunnerHTTPBody(w, r, maxBytes, deadline)
}

func readRunnerHTTPBody(w http.ResponseWriter, r *http.Request, maxBytes int64, deadline time.Time) ([]byte, *RunnerOperationError) {
	controller := http.NewResponseController(w)
	interrupted := make(chan struct{})
	var canceledRead atomic.Bool
	stop := context.AfterFunc(r.Context(), func() {
		defer close(interrupted)
		canceledRead.Store(time.Now().Before(deadline))
		if err := controller.SetReadDeadline(time.Now()); err != nil {
			logRunnerError("interrupting runner operation body admission", "error", err)
		}
	})
	data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if !stop() {
		<-interrupted
	}
	if canceledRead.Load() {
		return nil, &RunnerOperationError{http.StatusRequestTimeout, "input_canceled", fmt.Errorf("runner input canceled: %w", r.Context().Err())}
	}
	if timeout, ok := errors.AsType[net.Error](readErr); ok && timeout.Timeout() {
		return nil, &RunnerOperationError{http.StatusRequestTimeout, "input_timeout", errors.Join(context.DeadlineExceeded, readErr)}
	}
	if err := r.Context().Err(); err != nil {
		return nil, &RunnerOperationError{http.StatusRequestTimeout, "input_canceled", errors.Join(err, readErr)}
	}
	if readErr != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
			return nil, &RunnerOperationError{http.StatusRequestEntityTooLarge, "request_too_large", readErr}
		}
		return nil, &RunnerOperationError{http.StatusBadRequest, "input_read", fmt.Errorf("reading runner request: %w", readErr)}
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		return nil, &RunnerOperationError{http.StatusInternalServerError, "input_deadline_reset", fmt.Errorf("resetting runner read deadline: %w", err)}
	}
	return data, nil
}

// WriteRunnerOperationJSON writes one capture-approved, caller-validated result
// in the bounded delivery phase. It never retries or proves consumer receipt.
func WriteRunnerOperationJSON(w http.ResponseWriter, status, exitCode int, data []byte, inputComplete bool) error {
	if !json.Valid(data) {
		return fmt.Errorf("runner operation output is not valid JSON")
	}
	if !inputComplete {
		w.Header().Set("Connection", "close")
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		logRunnerError("setting runner operation write deadline; aborting transport without output", "error", err)
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Waza-Exit-Code", strconv.Itoa(exitCode))
	w.WriteHeader(status)
	payload := append(bytes.Clone(bytes.TrimRight(data, " \t\r\n")), '\n')
	written, err := w.Write(payload)
	if err != nil {
		return fmt.Errorf("writing runner operation output: %w", err)
	}
	if written != len(payload) {
		return fmt.Errorf("writing runner operation output: %w", io.ErrShortWrite)
	}
	if err := controller.Flush(); err != nil {
		return fmt.Errorf("flushing runner operation output: %w", err)
	}
	return nil
}
