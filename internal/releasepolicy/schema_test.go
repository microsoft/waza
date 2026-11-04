package releasepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestReleaseSchemaAdmitsRecordedHostLifecycle(t *testing.T) {
	var schemaDoc any
	if err := json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &schemaDoc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("release.json", schemaDoc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("release.json#/$defs/releasepolicy_ActualRunRow")
	if err != nil {
		t.Fatal(err)
	}
	exitCode, end, complete, success := int64(3), 3, 2, true
	row := ActualRunRow{Run: models.RunResult{RunNumber: 1, HostLifecycle: &models.HostLifecycle{
		ModelTurns: []models.ModelTurn{{TurnID: "0", StartIndex: 0, EndIndex: &end}},
		Shells: []models.ShellLifecycle{{ShellID: "build",
			Calls: []models.ShellToolCall{{ToolCallID: "start", ToolName: "bash", Role: models.ShellCallStart, StartIndex: 1, CompleteIndex: &complete, Success: &success}},
			Exits: []models.ShellExitEvent{{Source: models.ShellExitSourceNotification, Kind: "shell_completed", ExitCode: &exitCode, Index: 4}},
		}},
	}}}
	decode := func() map[string]any {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	value := decode()
	if err := schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	object := func(v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("expected object, got %T", v)
		}
		return m
	}
	first := func(v any) any {
		t.Helper()
		items, ok := v.([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("expected nonempty array, got %T", v)
		}
		return items[0]
	}
	lifecycle := object(object(value["run"])["host_lifecycle"])
	object(first(object(first(lifecycle["shells"]))["exits"]))["surprise"] = true
	if err := schema.Validate(value); err == nil {
		t.Fatal("structural schema admitted unknown host lifecycle property")
	}
	value = decode()
	delete(object(object(value["run"])["host_lifecycle"]), "shells")
	if err := schema.Validate(value); err == nil {
		t.Fatal("structural schema admitted host lifecycle without shells")
	}
}

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
