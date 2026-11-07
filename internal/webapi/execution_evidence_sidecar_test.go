package webapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/stretchr/testify/require"
)

type evidenceFailureBackend struct {
	reads int
	err   error
}

func (b *evidenceFailureBackend) Create(context.Context, string, []byte) (bool, error) {
	return false, errors.New("read-only test backend must not receive writes")
}

func (b *evidenceFailureBackend) Get(context.Context, string, int64) ([]byte, error) {
	b.reads++
	if b.err != nil {
		return nil, b.err
	}
	return nil, errors.New("private source payload /operator/private-store https://private.example/container")
}

func TestExecutionEvidenceStoredCorruptionWinsOverNestedInvalidDeclaration(t *testing.T) {
	backend := &evidenceFailureBackend{err: fmt.Errorf("%w: %w", artifactmanifest.ErrCorrupt, artifactmanifest.ErrInvalid)}
	mux := http.NewServeMux()
	RegisterEvidenceRoutesWithBackend(mux, backend)
	for _, suffix := range []string{"", "/document", "/csv"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+strings.Repeat("a", 64)+suffix, nil))
		require.Equal(t, http.StatusConflict, w.Code)
		require.Contains(t, w.Body.String(), `"code":"corrupt_source"`)
		require.NotContains(t, w.Body.String(), `"document"`)
	}
}

func TestExecutionEvidenceRoutesWithholdStorageFailuresAndValidateBeforeRead(t *testing.T) {
	backend := &evidenceFailureBackend{}
	mux := http.NewServeMux()
	RegisterEvidenceRoutesWithBackend(mux, backend)
	for _, suffix := range []string{"", "/document", "/csv"} {
		reads := backend.reads
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/invalid"+suffix, nil))
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Equal(t, reads, backend.reads)
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+strings.Repeat("a", 64)+suffix, nil))
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		require.NotContains(t, w.Body.String(), "private")
		require.NotContains(t, w.Body.String(), `"document"`)
		require.NotContains(t, w.Header().Get("Content-Type"), "text/csv")
	}
	require.Equal(t, 3, backend.reads)
}

func TestExecutionEvidenceRoutesRejectSuccessShapedIncompleteReceipt(t *testing.T) {
	mux := http.NewServeMux()
	registerEvidenceRoutes(mux, func(context.Context, string) (evidencestate.Inspection, error) {
		return evidencestate.Inspection{Integrity: "verified", Sources: []artifactmanifest.Receipt{}, Diagnostics: []artifactmanifest.Diagnostic{}}, nil
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+strings.Repeat("a", 64)+"/document", nil))
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "invalid_evidence_receipt")
}
