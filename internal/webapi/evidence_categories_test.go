package webapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
)

func TestExecutionEvidenceCategoryRouteRejectsSuccessShapedIncompleteReceipt(t *testing.T) {
	mux := http.NewServeMux()
	registerEvidenceCategoryRoute(mux, func(context.Context, string) (evidencestate.Inspection, []evidencestate.CategoryEvidence, error) {
		count := int64(1)
		return evidencestate.Inspection{Integrity: "verified"},
			[]evidencestate.CategoryEvidence{{Category: evidencestate.Usage, State: evidencestate.Present, ObservedCount: &count}}, nil
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/evidence/"+strings.Repeat("a", 64)+"/categories", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("incomplete success-shaped receipt returned %d", w.Code)
	}
	var result struct {
		evidencestate.Inspection
		Categories []evidencestate.CategoryEvidence `json:"categories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Document != nil || len(result.Categories) != 6 {
		t.Fatal("incomplete receipt retained a document or omitted unavailable categories")
	}
	for _, category := range result.Categories {
		if category.State != evidencestate.Unavailable || category.ObservedCount != nil || category.Usage != nil {
			t.Fatal("rejected receipt manufactured authoritative counts or usage")
		}
	}
}
