package webapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/inventory"
)

func TestInventoryVerifyRoute(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	data, err := inventory.Generate(inventory.GenerateInput{
		SubjectKind: inventory.SubjectImage, SubjectName: "ghcr.io/microsoft/waza-runner:0.42.0", SubjectDigest: digest,
		Components: []inventory.Component{{Type: "library", Name: "bash", Version: "5.2.15-2+b7", PURL: "pkg:deb/debian/bash@5.2.15-2+b7"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterInventoryRoutes(mux)
	post := func(body any, contentType string) (*httptest.ResponseRecorder, map[string]any) {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/verify", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var report map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &report)
		return rec, report
	}
	expect := map[string]string{"kind": "image", "digest": digest, "name": "ghcr.io/microsoft/waza-runner@" + digest}
	rec, report := post(map[string]any{"inventory": data, "expect": expect}, "application/json")
	if rec.Code != http.StatusOK || report["status"] != "verified" || rec.Header().Get("X-Waza-Exit-Code") != "0" {
		t.Fatalf("expected verified: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]map[string]any{
		"mismatched": {"inventory": data, "expect": map[string]string{"kind": "image", "digest": "sha256:" + strings.Repeat("e", 64)}},
		"missing":    {"expect": expect},
		"altered":    {"inventory": bytes.Replace(data, []byte("bash@5.2.15-2+b7\""), []byte("bash@9.9\""), 1), "expect": expect},
	} {
		rec, report := post(body, "application/json")
		if rec.Code != http.StatusOK || report["status"] != "failed" || rec.Header().Get("X-Waza-Exit-Code") != "1" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec, _ := post(map[string]any{"inventory": data}, "text/plain"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON request = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inventory/verify", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed request = %d", rec.Code)
	}
}
