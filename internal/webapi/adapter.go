package webapi

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/runneradapter"
)

// RegisterAdapterRoutes exposes offline adapter translation. It never
// dispatches: callers send the returned call to the public route themselves.
func RegisterAdapterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/adapter/translate", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("setting adapter read deadline: %v", err))
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "adapter translation requires Content-Type: application/json")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, runneradapter.MaxDescriptorBytes+1))
		translation := runneradapter.Translate(data)
		if err != nil {
			translation = runneradapter.Unreadable(data, err)
		}
		encoded, err := runneradapter.EncodeTranslation(translation)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "adapter translation failed its published schema")
			return
		}
		status := http.StatusOK
		if translation.Status != "translated" {
			status = http.StatusUnprocessableEntity
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(append(encoded, '\n'))
	})
}
