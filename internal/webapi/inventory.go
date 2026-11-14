package webapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/waza/internal/inventory"
)

// maxInventoryRequestBytes bounds one upload: a base64 inventory at the
// verifier's own limit plus the expectation.
const maxInventoryRequestBytes = inventory.MaxInventoryBytes/3*4 + 64<<10

type inventoryVerifyRequest struct {
	// Inventory holds the exact inventory bytes (base64); null or absent
	// reports a missing inventory rather than a request error.
	Inventory []byte                `json:"inventory"`
	Expect    inventory.Expectation `json:"expect"`
}

// RegisterInventoryRoutes adds offline verification of an uploaded component
// inventory against the image or plugin digest the caller consumed. Importing
// the inventory package also registers the component-inventory provenance
// binding verifier for the provenance route.
func RegisterInventoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/inventory/verify", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("setting read deadline: %v", err))
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "inventory verification requires Content-Type: application/json")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInventoryRequestBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("reading inventory upload: %v; inventories are limited to 16 MiB", err))
			return
		}
		var req inventoryVerifyRequest
		if err := json.Unmarshal(data, &req); err != nil {
			writeError(w, http.StatusBadRequest, "body must be {inventory: base64, expect: {kind, digest, name}}")
			return
		}
		switch req.Expect.Kind {
		case "":
			req.Expect.Kind = inventory.SubjectImage
		case inventory.SubjectImage, inventory.SubjectPlugin:
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("expect.kind must be %q or %q", inventory.SubjectImage, inventory.SubjectPlugin))
			return
		}
		if req.Expect.Kind == inventory.SubjectImage && req.Expect.Name != "" {
			req.Expect.Name = inventory.ImageRepository(req.Expect.Name)
		}
		report := inventory.Verify(req.Inventory, req.Expect)
		encoded, err := inventory.EncodeReport(report)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Waza-Exit-Code", strconv.Itoa(report.ExitCode))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	})
}
