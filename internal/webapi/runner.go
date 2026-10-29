package webapi

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
)

// RegisterRunnerRoutes exposes offline negotiation only. No inputs, runtime,
// artifacts, filesystem discovery or model credentials are consumed.
func RegisterRunnerRoutes(mux *http.ServeMux) {
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
}
