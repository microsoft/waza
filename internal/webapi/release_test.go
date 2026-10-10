package webapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReleaseArtifactIsolation(t *testing.T) {
	dir := t.TempDir()
	legacy, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"waza.release-policy", "waza.release-resolved-plan", "waza.release-receipt",
		"waza.release-journal", "waza.release-results", "waza.release-result-binding", "waza.release-decision",
		"waza.release-future"} {
		data := `{"kind":"` + kind + `","tasks":[],"eval_id":"not-a-run","version":"1.0"}`
		if err := os.WriteFile(filepath.Join(dir, kind+".json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := NewFileStore(dir).ListRuns("", "")
	if err != nil || len(runs) != 1 || runs[0].ID != "compatibility-run" {
		t.Fatalf("strict sidecars contaminated ordinary outcomes: %+v %v", runs, err)
	}
}

func TestReleaseAPIMissingInvalidAndSymlink(t *testing.T) {
	for _, name := range []string{"empty", "invalid", "symlink", "missing_root"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mux := http.NewServeMux()
			switch name {
			case "invalid":
				if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "policy.json")); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			case "missing_root":
				dir = filepath.Join(dir, "missing")
			}
			RegisterReleaseRoutes(mux, dir)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
			want := http.StatusOK
			if name == "symlink" || name == "missing_root" {
				want = http.StatusInternalServerError
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if name == "invalid" {
				var collections []ReleaseCollection
				if err := json.Unmarshal(response.Body.Bytes(), &collections); err != nil {
					t.Fatal(err)
				}
				if len(collections) != 1 || collections[0].Decision.Accepted || collections[0].Error == "" ||
					collections[0].Decision.Compatibility.State != "invalid" {
					t.Fatalf("invalid collection looked successful: %+v", collections)
				}
			}
		})
	}
}

func TestReleaseUsageRetainsExactTokens(t *testing.T) {
	value := json.Number("9007199254740993")
	d := releasepolicy.InitialDecision()
	d.Usage[releasepolicy.Baseline] = []releasepolicy.UsageAxis{{Axis: "input_tokens",
		Availability: "available", Value: &value, Observation: "final_complete_attributable"}}
	data, err := json.Marshal(releaseView(d))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"value":"9007199254740993"`) {
		t.Fatalf("JavaScript transport lost exact token count: %s", data)
	}
	var definition, view any
	if err := json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &definition); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("release.json", definition); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(view); err != nil {
		t.Fatalf("exact-number API view disagrees with its published schema: %v", err)
	}
}
