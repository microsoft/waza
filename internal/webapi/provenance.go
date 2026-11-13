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

	"github.com/microsoft/waza/internal/provenance"
)

// maxProvenanceRequestBytes bounds one uploaded bundle (base64 members plus key).
const maxProvenanceRequestBytes = 48 << 20

type provenanceVerifyRequest struct {
	PublicKey string            `json:"public_key"`
	Expect    provenanceExpect  `json:"expect"`
	Files     map[string][]byte `json:"files"`
}

type provenanceExpect struct {
	ImageDigest  string `json:"image_digest"`
	WazaVersion  string `json:"waza_version"`
	PolicySHA256 string `json:"policy_sha256"`
}

// RegisterProvenanceRoutes adds offline verification of an uploaded bundle.
// Members arrive in the request body; no host paths are read and the trust
// anchor is the caller-supplied public key, never a bundle member.
func RegisterProvenanceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/provenance/verify", func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("setting read deadline: %v", err))
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "provenance verification requires Content-Type: application/json")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProvenanceRequestBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("reading bundle upload: %v", err))
			return
		}
		var req provenanceVerifyRequest
		if err := json.Unmarshal(data, &req); err != nil || len(req.Files) == 0 {
			writeError(w, http.StatusBadRequest, "body must be {public_key, expect, files{path: base64}} with at least one file")
			return
		}
		src, err := provenance.MapSource(req.Files)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		opts := provenance.Options{
			ExpectImageDigest:  req.Expect.ImageDigest,
			ExpectWazaVersion:  req.Expect.WazaVersion,
			ExpectPolicySHA256: req.Expect.PolicySHA256,
		}
		if req.PublicKey != "" {
			verifier, err := provenance.NewLocalVerifier([]byte(req.PublicKey))
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("public key: %v", err))
				return
			}
			opts.Verifier = verifier
		}
		report := provenance.Verify(src, opts)
		encoded, err := provenance.EncodeReport(report)
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
