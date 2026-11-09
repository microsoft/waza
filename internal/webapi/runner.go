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
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

var runnerDiagnosticActive atomic.Bool
var runnerDiagnosticsSuppressed atomic.Uint64

func logRunnerError(message string, args ...any) {
	// A blocked logging sink may retain one reporter, never one per request.
	if !runnerDiagnosticActive.CompareAndSwap(false, true) {
		runnerDiagnosticsSuppressed.Add(1)
		return
	}
	logger := slog.Default()
	go func() {
		defer runnerDiagnosticActive.Store(false)
		args = append(args, "suppressed_diagnostics", runnerDiagnosticsSuppressed.Swap(0))
		logger.Error(message, args...)
	}()
}

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
					logRunnerError("interrupting runner body admission", "error", err)
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

type terminalRunner interface {
	RunTerminal(context.Context, []byte) (runnerterminal.Envelope, *runnerterminal.Binding)
}

func runTerminal(w http.ResponseWriter, r *http.Request, service terminalRunner) {
	controller := http.NewResponseController(w)
	inputComplete := false
	emit := func(status int, envelope runnerterminal.Envelope, binding *runnerterminal.Binding) {
		if !inputComplete {
			// Do not drain an unadmitted partial body before delivering its
			// rejection; the peer may never finish sending it.
			w.Header().Set("Connection", "close")
		}
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			logRunnerError("setting runner terminal write deadline; aborting transport without output",
				"outcome", envelope.Outcome, "boundary", envelope.FailureBoundary, "error", err)
			// Returning normally would let net/http write an implicit success.
			// No response operation is safe without the delivery deadline.
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Waza-Exit-Code", strconv.Itoa(envelope.ExitCode))
		w.WriteHeader(status)
		if _, err := runnerterminal.NewEmitter(w).Emit(envelope, binding); err != nil {
			logRunnerError("writing runner terminal output; no retry", "error", err)
			return
		}
		if err := controller.Flush(); err != nil {
			logRunnerError("flushing runner terminal output; no retry", "error", err)
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
	// Probe deadline support without starting an HTTP/2 stream timer during
	// admission or execution. Only emit owns the bounded delivery phase.
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		emit(http.StatusInternalServerError, runnerterminal.NewFailure(runnerterminal.InfrastructureFailure, "output", "not_checked",
			"output_deadline", fmt.Sprintf("Probing runner write deadline support: %.600s", err)), nil)
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
			logRunnerError("interrupting runner terminal body admission", "error", err)
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
	if request, err := runnercontract.Parse(data); err == nil && slices.Contains(request.RequiredCapabilities, "inline.copilot-text") {
		if err := authorizeNativeRunnerHTTP(r); err != nil {
			emit(http.StatusForbidden, runnerterminal.NewFailure(runnerterminal.InvalidInput, "input", "not_checked",
				"native_http_denied", err.Error()), nil)
			return
		}
	}
	envelope, binding := service.RunTerminal(r.Context(), data)
	emit(http.StatusOK, envelope, binding)
}

// Paid execution is local-only. Neither forwarded headers nor a request's
// paid-consent flag authorize a remote peer or an arbitrary browser origin.
func authorizeNativeRunnerHTTP(r *http.Request) error {
	deny := fmt.Errorf("native HTTP execution requires a loopback peer, a literal loopback Host, and a same-origin browser request; use waza runner run locally")
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return deny
	}
	address, err := netip.ParseAddr(peer)
	if err != nil || !address.Unmap().IsLoopback() {
		return deny
	}
	authority, err := url.Parse("http://" + r.Host)
	if err != nil || authority.User != nil || authority.Host != r.Host || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" {
		return deny
	}
	host := authority.Hostname()
	if !strings.EqualFold(host, "localhost") {
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Unmap().IsLoopback() {
			return deny
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return deny
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if origin != scheme+"://"+r.Host {
			return deny
		}
	}
	return nil
}
