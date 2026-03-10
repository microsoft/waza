package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBenchmarkSpec_PolymorphicGraderParameters(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `name: typed-graders
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
graders:
  - name: text-check
    type: text
    config:
      contains:
        - "hello"
  - name: prompt-check
    type: prompt
    config:
      prompt: "judge this output"
      continue_session: true
  - name: future-check
    type: custom_future
    config:
      some_flag: true
metrics: []
tasks: []
`

	specPath := filepath.Join(tempDir, "spec.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write spec file: %v", err)
	}

	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("LoadBenchmarkSpec: %v", err)
	}

	textParams, ok := spec.Graders[0].Parameters.(TextGraderParameters)
	if !ok {
		t.Fatalf("expected TextGraderParameters, got %T", spec.Graders[0].Parameters)
	}
	if len(textParams.Contains) != 1 || textParams.Contains[0] != "hello" {
		t.Fatalf("unexpected text params: %#v", textParams)
	}

	promptParams, ok := spec.Graders[1].Parameters.(PromptGraderParameters)
	if !ok {
		t.Fatalf("expected PromptGraderParameters, got %T", spec.Graders[1].Parameters)
	}
	if promptParams.Prompt != "judge this output" || !promptParams.ContinueSession {
		t.Fatalf("unexpected prompt params: %#v", promptParams)
	}

	genericParams, ok := spec.Graders[2].Parameters.(GenericGraderParameters)
	if !ok {
		t.Fatalf("expected GenericGraderParameters, got %T", spec.Graders[2].Parameters)
	}
	flag, ok := genericParams["some_flag"].(bool)
	if !ok || !flag {
		t.Fatalf("unexpected generic params: %#v", genericParams)
	}
}

func TestLoadTestCase_PolymorphicValidatorParameters(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `id: test-001
name: Test
inputs:
  prompt: "say hello"
graders:
  - name: inline-check
    type: code
    config:
      assertions:
        - "len(output) > 0"
      language: javascript
  - name: schema-check
    type: json_schema
    config:
      schema:
        type: object
`

	testPath := filepath.Join(tempDir, "test.yaml")
	if err := os.WriteFile(testPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write test case file: %v", err)
	}

	tc, err := LoadTestCase(testPath)
	if err != nil {
		t.Fatalf("LoadTestCase: %v", err)
	}

	inlineParams, ok := tc.Validators[0].Parameters.(InlineScriptGraderParameters)
	if !ok {
		t.Fatalf("expected InlineScriptGraderParameters, got %T", tc.Validators[0].Parameters)
	}
	if inlineParams.Language != "javascript" || len(inlineParams.Assertions) != 1 {
		t.Fatalf("unexpected inline params: %#v", inlineParams)
	}

	schemaParams, ok := tc.Validators[1].Parameters.(JSONSchemaGraderParameters)
	if !ok {
		t.Fatalf("expected JSONSchemaGraderParameters, got %T", tc.Validators[1].Parameters)
	}
	typeVal, ok := schemaParams.Schema["type"].(string)
	if !ok || typeVal != "object" {
		t.Fatalf("unexpected schema params: %#v", schemaParams)
	}
}
