package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
)

const classifierValidBody = `name: classifier-test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
`

func classifierWriteFile(t *testing.T, pattern, data string) string {
	t.Helper()
	file, err := os.CreateTemp(".", pattern)
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(path); err != nil {
			t.Errorf("remove test file: %v", err)
		}
	})
	if _, err := file.WriteString(data); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close test file: %v", closeErr)
		}
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifyEvalSpecHeaderParity(t *testing.T) {
	explicitScenarioError := "scenario requires explicit schemaVersion 2.0; older executables must reject scenario semantics"
	emptyScenarioError := "scenario must be a non-empty workflow identity"
	tests := []struct {
		name         string
		header       string
		version      string
		scenario     string
		exactError   string
		yamlError    string
		invalidMajor string
	}{
		{name: "omitted version", version: CurrentSchemaVersion},
		{name: "legacy 1.0", header: "schemaVersion: '1.0'\n", version: "1.0"},
		{name: "future same-major 1.99", header: "schemaVersion: '1.99'\n", version: "1.99"},
		{name: "empty version", header: "schemaVersion: ''\n", version: CurrentSchemaVersion},
		{name: "null version", header: "schemaVersion: null\n", version: CurrentSchemaVersion},
		{name: "blank version", header: "schemaVersion: '  '\n", version: CurrentSchemaVersion},
		{name: "explicit scenario", header: "schemaVersion: '2.0'\nscenario: checkout-recovery\n", version: "2.0", scenario: "checkout-recovery"},
		{name: "scenario identity preserved", header: "schemaVersion: '2.0'\nscenario: ' checkout-recovery '\n", version: "2.0", scenario: " checkout-recovery "},
		{name: "null legacy scenario", header: "scenario: null\n", version: CurrentSchemaVersion},
		{name: "null explicit legacy scenario", header: "schemaVersion: '1.0'\nscenario: null\n", version: "1.0"},
		{name: "empty scenario", header: "schemaVersion: '2.0'\nscenario: ''\n", exactError: emptyScenarioError},
		{name: "blank scenario", header: "schemaVersion: '2.0'\nscenario: '  '\n", exactError: emptyScenarioError},
		{name: "whitespace scenario", header: "schemaVersion: '2.0'\nscenario: \"\\t\\n\"\n", exactError: emptyScenarioError},
		{name: "empty scenario before version validation", header: "schemaVersion: '3.0'\nscenario: ''\n", exactError: emptyScenarioError},
		{name: "missing explicit scenario version", header: "scenario: recovery\n", exactError: explicitScenarioError},
		{name: "legacy scenario version", header: "schemaVersion: '1.99'\nscenario: recovery\n", exactError: explicitScenarioError},
		{name: "future scenario minor", header: "schemaVersion: '2.1'\nscenario: recovery\n", exactError: explicitScenarioError},
		{name: "future scenario major", header: "schemaVersion: '3.0'\nscenario: recovery\n", exactError: explicitScenarioError},
		{name: "scenario version without scenario", header: "schemaVersion: '2.0'\n", invalidMajor: "2.0"},
		{name: "scenario version with null scenario", header: "schemaVersion: '2.0'\nscenario: null\n", invalidMajor: "2.0"},
		{name: "future version without scenario", header: "schemaVersion: '2.1'\n", invalidMajor: "2.1"},
		{name: "future major without scenario", header: "schemaVersion: '3.0'\n", invalidMajor: "3.0"},
		{name: "malformed version", header: "schemaVersion: '1'\n", exactError: `eval.yaml {path} has invalid schemaVersion "1": expected MAJOR.MINOR`},
		{name: "version mapping", header: "schemaVersion: {major: 1}\n", yamlError: "cannot unmarshal !!map into string"},
		{name: "version sequence", header: "schemaVersion: [1, 0]\n", yamlError: "cannot unmarshal !!seq into string"},
		{name: "scenario mapping", header: "schemaVersion: '2.0'\nscenario: {name: recovery}\n", yamlError: "cannot unmarshal !!map into string"},
		{name: "scenario sequence", header: "schemaVersion: '2.0'\nscenario: [recovery]\n", yamlError: "cannot unmarshal !!seq into string"},
		{name: "malformed YAML", header: "schemaVersion: [\n", yamlError: "yaml:"},
		{name: "duplicate version", header: "schemaVersion: '1.0'\nschemaVersion: '1.99'\n", yamlError: `mapping key "schemaVersion" already defined`},
		{name: "duplicate scenario", header: "schemaVersion: '2.0'\nscenario: one\nscenario: two\n", yamlError: `mapping key "scenario" already defined`},
		{name: "duplicate unrelated top-level key", header: "name: duplicate\n", yamlError: `mapping key "name" already defined`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.header + classifierValidBody)
			path := classifierWriteFile(t, "eval-classifier-*.yaml", string(data))
			version, scenario, classErr := ClassifyEvalSpec(data, path)
			parsed, parseErr := ParseEvalSpec(data, path)
			loaded, loadErr := LoadEvalSpec(path)
			if tt.exactError == "" && tt.yamlError == "" && tt.invalidMajor == "" {
				if classErr != nil || parseErr != nil || loadErr != nil {
					t.Fatalf("classify/parse/load errors: %v / %v / %v", classErr, parseErr, loadErr)
				}
				if version != tt.version || scenario != tt.scenario {
					t.Fatalf("classification = (%q, %q), want (%q, %q)", version, scenario, tt.version, tt.scenario)
				}
				if parsed.SchemaVersion != version || parsed.Scenario != scenario {
					t.Errorf("parsed header = (%q, %q), classification = (%q, %q)", parsed.SchemaVersion, parsed.Scenario, version, scenario)
				}
				if !reflect.DeepEqual(parsed, loaded) {
					t.Errorf("byte parse differs from file load:\nparsed: %#v\nloaded: %#v", parsed, loaded)
				}
				return
			}
			if classErr == nil || parseErr == nil || loadErr == nil {
				t.Fatalf("expected classify/parse/load errors, got %v / %v / %v", classErr, parseErr, loadErr)
			}
			if version != "" || scenario != "" || parsed != nil || loaded != nil {
				t.Errorf("error returned partial results: (%q, %q), %v, %v", version, scenario, parsed, loaded)
			}
			if classErr.Error() != parseErr.Error() || classErr.Error() != loadErr.Error() {
				t.Errorf("diagnostics differ:\nclassify: %v\nparse: %v\nload: %v", classErr, parseErr, loadErr)
			}
			expected := strings.ReplaceAll(tt.exactError, "{path}", path)
			if tt.invalidMajor != "" {
				major := strings.Split(tt.invalidMajor, ".")[0]
				expected = fmt.Sprintf("eval.yaml %s uses schemaVersion %q (major %s), but this waza supports schema major 1 (current schemaVersion %s); run \"waza migrate <file>\" to migrate the artifact file", path, tt.invalidMajor, major, CurrentSchemaVersion)
			}
			if expected != "" && classErr.Error() != expected {
				t.Errorf("diagnostic = %q, want %q", classErr.Error(), expected)
			}
			if tt.yamlError != "" {
				prefix := fmt.Sprintf("parsing eval spec YAML (%s): ", path)
				if !strings.HasPrefix(classErr.Error(), prefix) || !strings.Contains(classErr.Error(), tt.yamlError) {
					t.Errorf("YAML diagnostic = %q, want prefix %q and detail %q", classErr.Error(), prefix, tt.yamlError)
				}
			}
		})
	}
}

func TestByteParseEvalSpecLegacyValues(t *testing.T) {
	body := `name: legacy-values
description: unchanged decoding
skill: code-explainer
version: '0.7'
config:
  trials_per_task: 3
  timeout_seconds: 120
  first_event_timeout_seconds: 10
  executor: mock
  model: test-model
  parallel: true
  workers: 2
  fail_fast: true
  skill_directories: [skills]
  instruction_files: [AGENTS.md]
  inject_skill_body: false
  disabled_skills: [other-skill]
  max_attempts: 4
inputs: {language: Go}
tasks_from: tasks.csv
range: [1, 3]
baseline: true
graders:
  - name: text-check
    type: text
    weight: 2
    config:
      contains: [explanation]
      not_contains: [secret]
metrics:
  - name: quality
    weight: 1
    threshold: 0.8
    enabled: true
    description: expected quality
tasks: [tasks/explain.yaml]
legacy_extension: retained compatibility
`
	for _, declared := range []string{"", "1.0", "1.99"} {
		t.Run("version="+declared, func(t *testing.T) {
			header := ""
			version := CurrentSchemaVersion
			if declared != "" {
				header = fmt.Sprintf("schemaVersion: %q\n", declared)
				version = declared
			}
			data := []byte(header + body)
			path := classifierWriteFile(t, "eval-byte-parse-*.yaml", string(data))
			inject := false
			expected := &EvalSpec{
				SchemaVersion: version,
				SpecIdentity:  SpecIdentity{Name: "legacy-values", Description: "unchanged decoding"},
				SkillName:     "code-explainer",
				Version:       "0.7",
				Config: Config{
					TrialsPerTask: 3, TimeoutSec: 120, FirstEventTimeoutSec: 10,
					EngineType: "mock", ModelID: "test-model", Concurrent: true, Workers: 2,
					StopOnError: true, SkillPaths: []string{"skills"}, InstructionFiles: []string{"AGENTS.md"},
					InjectSkillBody: &inject, DisabledSkills: []string{"other-skill"}, MaxAttempts: 4,
				},
				Inputs: map[string]string{"language": "Go"}, TasksFrom: "tasks.csv", Range: [2]int{1, 3}, Baseline: true,
				Graders: []GraderConfig{{
					Identifier: "text-check", Kind: GraderKindText, Weight: 2,
					Parameters: TextGraderParameters{Contains: []string{"explanation"}, NotContains: []string{"secret"}},
				}},
				Metrics: []MeasurementDef{{Identifier: "quality", Weight: 1, Threshold: 0.8, Enabled: true, Desc: "expected quality"}},
				Tasks:   []string{"tasks/explain.yaml"},
			}
			parsed, err := ParseEvalSpec(data, path)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadEvalSpec(path)
			if err != nil {
				t.Fatal(err)
			}
			for name, actual := range map[string]*EvalSpec{"parse": parsed, "load": loaded} {
				if !reflect.DeepEqual(actual, expected) {
					t.Errorf("%s changed legacy values:\ngot: %#v\nwant: %#v", name, actual, expected)
				}
			}
		})
	}
}

func TestClassifyEvalSpecDoesNotValidateUnrelatedParameters(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "wrong config type", body: "config: {trials_per_task: [1], timeout_seconds: 60}\n"},
		{name: "wrong grader parameters", body: classifierValidBody + "graders:\n  - name: wrong-type\n    type: tool_calls\n    config: {min_calls: [1]}\n"},
		{name: "invalid native matcher", body: classifierValidBody + "graders:\n  - name: invalid-matcher\n    type: tool_constraint\n    config:\n      expect_tools:\n        - tool: read_file\n          args:\n            path: {regex: '['}\n"},
		{name: "duplicate nested key", body: "config: {trials_per_task: 1, trials_per_task: 2, timeout_seconds: 60}\n"},
		{name: "invalid runtime values", body: "config: {trials_per_task: 0, timeout_seconds: 60}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("schemaVersion: '2.0'\nscenario: recovery\n" + tt.body)
			path := classifierWriteFile(t, "eval-classifier-unrelated-*.yaml", string(data))
			version, scenario, err := ClassifyEvalSpec(data, path)
			if err != nil || version != "2.0" || scenario != "recovery" {
				t.Fatalf("header classification = (%q, %q, %v)", version, scenario, err)
			}
			// Header classification intentionally is not full eval validation.
			parsed, parseErr := ParseEvalSpec(data, path)
			loaded, loadErr := LoadEvalSpec(path)
			if parseErr == nil || loadErr == nil || parsed != nil || loaded != nil {
				t.Fatalf("invalid eval unexpectedly accepted: parse (%v, %v), load (%v, %v)", parsed, parseErr, loaded, loadErr)
			}
			if parseErr.Error() != loadErr.Error() {
				t.Errorf("byte/file diagnostics differ: %q / %q", parseErr.Error(), loadErr.Error())
			}
		})
	}
}

func TestClassifyEvalSpecDoesNotLoadNativeMatcherResources(t *testing.T) {
	schemaPath := classifierWriteFile(t, "eval-classifier-schema-*.json", `{"type":"string","minLength":1}`)
	fileURI := testutil.FileURL(schemaPath)
	var fetches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"type":"string","minLength":1}`)); err != nil {
			t.Errorf("write spy response: %v", err)
		}
	}))
	defer server.Close()
	for _, kind := range []string{"tool_calls", "tool_constraint"} {
		for _, target := range []struct {
			name string
			uri  string
		}{{name: "file", uri: fileURI}, {name: "HTTP", uri: server.URL + "/schema.json"}} {
			t.Run(kind+"/"+target.name, func(t *testing.T) {
				list := "expect"
				if kind == "tool_constraint" {
					list = "expect_tools"
				}
				body := classifierValidBody + fmt.Sprintf(`graders:
  - name: resource-matcher
    type: %s
    config:
      %s:
        - tool: read_file
          args:
            path:
              json_schema:
                $ref: %q
`, kind, list, target.uri)
				for _, header := range []struct {
					name     string
					yaml     string
					version  string
					scenario string
				}{
					{name: "legacy", version: CurrentSchemaVersion},
					{name: "scenario", yaml: "schemaVersion: '2.0'\nscenario: recovery\n", version: "2.0", scenario: "recovery"},
				} {
					t.Run(header.name, func(t *testing.T) {
						data := []byte(header.yaml + body)
						original := append([]byte(nil), data...)
						// This is deliberately not a filename: classification must use
						// the provided bytes, not read the diagnostic source path.
						source := filepath.Join(schemaPath, "not-an-eval.yaml")
						version, scenario, err := ClassifyEvalSpec(data, source)
						if err != nil || version != header.version || scenario != header.scenario {
							t.Fatalf("classification = (%q, %q, %v), want (%q, %q, nil)", version, scenario, err, header.version, header.scenario)
						}
						if !reflect.DeepEqual(data, original) {
							t.Error("classification mutated input bytes")
						}
						if got := fetches.Load(); got != 0 {
							t.Fatalf("classification fetched HTTP schema %d times", got)
						}
					})
				}
				// Only file-backed configs are typed-decoded as a positive control.
				// Parse/Load on the HTTP case could intentionally perform I/O.
				if target.name == "file" {
					spec, err := ParseEvalSpec([]byte(body), schemaPath)
					if err != nil {
						t.Fatalf("legitimate file-backed matcher failed to decode: %v", err)
					}
					switch params := spec.Graders[0].Parameters.(type) {
					case ToolCallsGraderParameters:
						matcher := params.Expect[0].Args["path"]
						if !matcher.IsCompiled() {
							t.Error("file-backed tool_calls control did not compile")
						}
					case ToolConstraintGraderParameters:
						matcher := params.ExpectTools[0].Args["path"]
						if !matcher.IsCompiled() {
							t.Error("file-backed tool_constraint control did not compile")
						}
					default:
						t.Fatalf("unexpected native parameters: %T", params)
					}
				}
			})
		}
	}
	if got := fetches.Load(); got != 0 {
		t.Errorf("HTTP schema fetched %d times", got)
	}
}
