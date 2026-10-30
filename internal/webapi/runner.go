package webapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
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
			var report runnerunit.Report
			if action == "run" {
				report = service.Run(r.Context(), data)
			} else {
				report = runnerunit.Validate(data)
			}
			writeJSON(w, http.StatusOK, report)
		})
	}
}
