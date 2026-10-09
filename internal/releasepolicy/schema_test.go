package releasepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPublishedReleaseSchemas(t *testing.T) {
	var schemaDoc any
	if err := json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &schemaDoc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("release.json", schemaDoc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	p := testPolicy(t, 8)
	directory := collectTest(t, p, testCollector(p))
	for _, name := range []string{"policy.json", "baseline.begin.json", "candidate.begin.json",
		"journal.json", "baseline.final.json", "candidate.final.json", "baseline.result-binding.json",
		"candidate.result-binding.json", "baseline.results.json", "candidate.results.json"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); err != nil {
				t.Fatal(err)
			}
			value["surprise"] = true
			if err := schema.Validate(value); err == nil {
				t.Fatal("structural schema admitted unknown artifact property")
			}
		})
	}
	for _, value := range []any{InitialDecision(), p.Arms[Baseline].Plan} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(decoded); err != nil {
			t.Fatal(err)
		}
	}
}
