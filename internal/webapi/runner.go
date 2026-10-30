package webapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

// RegisterRunnerRoutes retains offline negotiation and adds explicit single-unit
// validation/execution. No host input paths or saved artifacts are consumed.
func RegisterRunnerRoutes(mux *http.ServeMux) {
	RegisterRunnerRoutesWithService(mux, runnerunit.NewService(nil))
}

func RegisterRunnerRoutesWithService(mux *http.ServeMux, service *runnerunit.Service) {
	mux.HandleFunc("GET /api/v1/runner/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, runnercontract.Capabilities())
	})
	mux.HandleFunc("POST /api/v1/runner/negotiate", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("setting negotiation read deadline: %v", err))
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, runnercontract.Rejected("runner negotiation requires Content-Type: application/json"))
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, runnercontract.MaxRequestBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, runnercontract.Rejected(fmt.Sprintf("reading negotiation request: %v", err)))
			return
		}
		writeJSON(w, http.StatusOK, runnercontract.Negotiate(data))
	})
	for _, action := range []string{"validate", "run"} {
		mux.HandleFunc("POST /api/v1/runner/"+action, func(w http.ResponseWriter, r *http.Request) {
			if action == "run" {
				runTerminal(w, r, service)
				return
			}
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("setting runner read deadline: %v", err))
				return
			}
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "runner requires Content-Type: application/json")
				return
			}
			interrupted := make(chan struct{})
			stop := context.AfterFunc(r.Context(), func() {
				defer close(interrupted)
				if err := controller.SetReadDeadline(time.Now()); err != nil {
					slog.Error("interrupting runner body admission", "error", err)
				}
			})
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, runnercontract.MaxRequestBytes))
			if !stop() {
				<-interrupted
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("reading runner request: %v", err))
				return
			}
			if err := r.Context().Err(); err != nil {
				writeError(w, http.StatusRequestTimeout, fmt.Sprintf("runner admission canceled: %v", err))
				return
			}
			report := runnerunit.Validate(data)
			writeJSON(w, http.StatusOK, report)
		})
	}
}

func runTerminal(w http.ResponseWriter, r *http.Request, service *runnerunit.Service) {
	controller := http.NewResponseController(w)
	inputComplete := false
	emit := func(status int, envelope runnerterminal.Envelope, binding *runnerterminal.Binding) {
		if !inputComplete {
			// Do not drain an unadmitted partial body before delivering its
			// rejection; the peer may never finish sending it.
			w.Header().Set("Connection", "close")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			message := fmt.Sprintf("Setting terminal write deadline failed after outcome %s at %s: %.600s",
				envelope.Outcome, envelope.FailureBoundary, err)
			if len(envelope.Diagnostics) == runnerterminal.MaxDiagnostics {
				envelope.Diagnostics = envelope.Diagnostics[:runnerterminal.MaxDiagnostics-1]
				message += "; one additional original diagnostic is unavailable"
			}
			envelope.Outcome, envelope.ExitCode, envelope.FailureBoundary = runnerterminal.InfrastructureFailure, 2, "output"
			envelope.Diagnostics = append(envelope.Diagnostics, runnerterminal.Diagnostic{Code: "output_deadline", Message: message})
			status = http.StatusInternalServerError
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Waza-Exit-Code", strconv.Itoa(envelope.ExitCode))
		w.WriteHeader(status)
		if _, err := runnerterminal.NewEmitter(w).Emit(envelope, binding); err != nil {
			slog.Error("writing runner terminal output; no retry", "error", err)
			return
		}
		if err := controller.Flush(); err != nil {
			slog.Error("flushing runner terminal output; no retry", "error", err)
		}
	}
	fail := func(status int, err error) {
		emit(status, runnerterminal.Failure("input", err), nil)
	}
	inputDeadline := time.Now().Add(5 * time.Second)
	if err := controller.SetReadDeadline(inputDeadline); err != nil {
		fail(http.StatusInternalServerError, fmt.Errorf("setting runner read deadline: %w", err))
		return
	}
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		emit(http.StatusInternalServerError, runnerterminal.NewFailure(runnerterminal.InfrastructureFailure, "output", "not_checked",
			"output_deadline", fmt.Sprintf("Setting runner write deadline: %.600s", err)), nil)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		emit(http.StatusUnsupportedMediaType, runnerterminal.NewFailure(runnerterminal.InvalidInput, "input", "not_checked",
			"invalid_media_type", "runner run requires Content-Type: application/json"), nil)
		return
	}
	interrupted := make(chan struct{})
	var canceledRead atomic.Bool
	stop := context.AfterFunc(r.Context(), func() {
		defer close(interrupted)
		canceledRead.Store(time.Now().Before(inputDeadline))
		if err := controller.SetReadDeadline(time.Now()); err != nil {
			slog.Error("interrupting runner terminal body admission", "error", err)
		}
	})
	data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, runnercontract.MaxRequestBytes))
	if !stop() {
		<-interrupted
	}
	if canceledRead.Load() {
		fail(http.StatusRequestTimeout, fmt.Errorf("runner input canceled: %w", r.Context().Err()))
		return
	}
	// net/http also cancels the request when its body read deadline expires.
	// Inspect the observed read error first so that timeout is not relabeled.
	if timeout, ok := errors.AsType[net.Error](readErr); ok && timeout.Timeout() {
		fail(http.StatusRequestTimeout, errors.Join(context.DeadlineExceeded, readErr))
		return
	}
	if err := r.Context().Err(); err != nil {
		fail(http.StatusRequestTimeout, errors.Join(err, readErr))
		return
	}
	if readErr != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
			emit(http.StatusRequestEntityTooLarge, runnerterminal.NewFailure(runnerterminal.InvalidInput, "input", "not_checked",
				"request_too_large", "runner request exceeds the 4 MiB limit"), nil)
		} else {
			fail(http.StatusBadRequest, fmt.Errorf("reading runner request: %w", readErr))
		}
		return
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		fail(http.StatusInternalServerError, fmt.Errorf("resetting runner read deadline: %w", err))
		return
	}
	inputComplete = true
	envelope, binding := service.RunTerminal(r.Context(), data)
	emit(http.StatusOK, envelope, binding)
}
