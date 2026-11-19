package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/provenance"
)

func TestProvenanceVerifyRoute(t *testing.T) {
	read := func(path string) []byte {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	privatePEM, publicPEM, _, err := provenance.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := provenance.NewLocalSigner(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := provenance.Generate(context.Background(), provenance.GenerateInput{
		Request:  read("../../examples/runner/unit.json"),
		Manifest: read("../artifactmanifest/testdata/publication-manifest.json"),
		Objects:  map[string][]byte{"terminal.json": read("../artifactmanifest/testdata/publication-terminal.json")},
		Policy:   []byte("{}"), PolicyName: "policy.json", PolicyMediaType: "application/json",
		WazaVersion: "0.42.0", ImageReference: "runner:test", ImageDigest: "sha256:" + strings.Repeat("c", 64),
	}, signer)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterProvenanceRoutes(mux)
	post := func(files map[string][]byte, contentType string) (*httptest.ResponseRecorder, map[string]any) {
		body, err := json.Marshal(map[string]any{"public_key": string(publicPEM), "files": files})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/provenance/verify", bytes.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var report map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &report)
		return rec, report
	}
	rec, report := post(bundle.Files, "application/json")
	if rec.Code != http.StatusOK || report["status"] != "verified" || rec.Header().Get("X-Waza-Exit-Code") != "0" {
		t.Fatalf("expected verified: %d %s", rec.Code, rec.Body.String())
	}
	bundle.Files["artifacts/objects/terminal.json"] = append(bytes.Clone(bundle.Files["artifacts/objects/terminal.json"]), ' ')
	rec, report = post(bundle.Files, "application/json")
	if rec.Code != http.StatusOK || report["status"] != "failed" || rec.Header().Get("X-Waza-Exit-Code") != "1" {
		t.Fatalf("expected failed: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ = post(map[string][]byte{"../escape": []byte("x")}, "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("path escape accepted: %d", rec.Code)
	}
	if rec, _ = post(bundle.Files, "text/plain"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON accepted: %d", rec.Code)
	}
}
