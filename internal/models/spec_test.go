package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBenchmarkSpec_LoadFromYAML(t *testing.T) {
	// Create temp YAML file
	tempDir := t.TempDir()
	yamlContent := `name: test-benchmark
description: Test benchmark spec
skill: test-skill
version: "1.0"
config:
  trials_per_task: 2
  timeout_seconds: 120
  executor: mock
  model: test-model
`
	specPath := filepath.Join(tempDir, "spec.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write spec file: %v", err)
	}

	// Load spec
	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	// Validate fields
	if spec.Name != "test-benchmark" {
		t.Errorf("Expected name 'test-benchmark', got '%s'", spec.Name)
	}
	if spec.SkillName != "test-skill" {
		t.Errorf("Expected skill 'test-skill', got '%s'", spec.SkillName)
	}
	if spec.Config.TrialsPerTask != 2 {
		t.Errorf("Expected 2 trials, got %d", spec.Config.TrialsPerTask)
	}
}

func TestTestCase_LoadFromYAML(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `id: test-001
name: Test Case
description: A test case
inputs:
  prompt: Test prompt
  context:
    key: value
expected:
  output_contains:
    - "result"
enabled: true
`
	testPath := filepath.Join(tempDir, "test.yaml")
	if err := os.WriteFile(testPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Load test case
	tc, err := LoadTestCase(testPath)
	if err != nil {
		t.Fatalf("Failed to load test case: %v", err)
	}

	// Validate
	if tc.TestID != "test-001" {
		t.Errorf("Expected ID 'test-001', got '%s'", tc.TestID)
	}
	if tc.DisplayName != "Test Case" {
		t.Errorf("Expected title 'Test Case', got '%s'", tc.DisplayName)
	}
	if tc.Active != nil && !*tc.Active {
		t.Error("Expected test to be active")
	}
}

func TestBenchmarkSpec_InputsDeserialization(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `name: inputs-test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
inputs:
  workspace_root: ./workspaces
  default_branch: main
  org: myorg
`
	specPath := filepath.Join(tempDir, "inputs.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write spec file: %v", err)
	}

	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	if len(spec.Inputs) != 3 {
		t.Fatalf("Expected 3 inputs, got %d", len(spec.Inputs))
	}

	expected := map[string]string{
		"workspace_root": "./workspaces",
		"default_branch": "main",
		"org":            "myorg",
	}
	for k, want := range expected {
		if got := spec.Inputs[k]; got != want {
			t.Errorf("Inputs[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestBenchmarkSpec_InputsOmittedWhenEmpty(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `name: no-inputs
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
`
	specPath := filepath.Join(tempDir, "no-inputs.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write spec file: %v", err)
	}

	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	if spec.Inputs != nil {
		t.Errorf("Expected nil Inputs when omitted, got %v", spec.Inputs)
	}
}

func TestBenchmarkSpec_DefaultValues(t *testing.T) {
	tempDir := t.TempDir()
	// Minimal YAML - defaults need to be set by loader
	yamlContent := `name: minimal
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
`
	specPath := filepath.Join(tempDir, "minimal.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write spec file: %v", err)
	}

	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	// Check loaded values
	if spec.Config.TrialsPerTask != 1 {
		t.Errorf("Expected trials=1, got %d", spec.Config.TrialsPerTask)
	}
	if spec.Config.TimeoutSec != 300 {
		t.Errorf("Expected timeout=300, got %d", spec.Config.TimeoutSec)
	}
	if spec.Config.EngineType != "mock" {
		t.Errorf("Expected engine='mock', got '%s'", spec.Config.EngineType)
	}
}

func TestGraderConfig_EffectiveWeight(t *testing.T) {
	tests := []struct {
		name   string
		weight float64
		want   float64
	}{
		{name: "zero defaults to 1.0", weight: 0, want: 1.0},
		{name: "negative defaults to 1.0", weight: -1, want: 1.0},
		{name: "explicit 1.0", weight: 1.0, want: 1.0},
		{name: "explicit 2.5", weight: 2.5, want: 2.5},
		{name: "explicit 0.5", weight: 0.5, want: 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gc := GraderConfig{Weight: tt.weight}
			got := gc.EffectiveWeight()
			if got != tt.want {
				t.Errorf("EffectiveWeight() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestBenchmarkSpec_GraderWeight(t *testing.T) {
	tempDir := t.TempDir()
	yamlContent := `name: weighted-graders
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
graders:
  - name: important
    type: text
    weight: 3.0
    config:
      regex_match: ["foo"]
  - name: minor
    type: text
    config:
      regex_match: ["bar"]
`
	specPath := filepath.Join(tempDir, "weighted.yaml")
	if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to write spec file: %v", err)
	}

	spec, err := LoadBenchmarkSpec(specPath)
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	if len(spec.Graders) != 2 {
		t.Fatalf("Expected 2 graders, got %d", len(spec.Graders))
	}

	if spec.Graders[0].Weight != 3.0 {
		t.Errorf("Expected grader[0] weight=3.0, got %f", spec.Graders[0].Weight)
	}
	if spec.Graders[0].EffectiveWeight() != 3.0 {
		t.Errorf("Expected grader[0] effective weight=3.0, got %f", spec.Graders[0].EffectiveWeight())
	}

	// Omitted weight should be zero-value, but EffectiveWeight returns 1.0
	if spec.Graders[1].Weight != 0 {
		t.Errorf("Expected grader[1] weight=0 (omitted), got %f", spec.Graders[1].Weight)
	}
	if spec.Graders[1].EffectiveWeight() != 1.0 {
		t.Errorf("Expected grader[1] effective weight=1.0, got %f", spec.Graders[1].EffectiveWeight())
	}
}

func TestBenchmarkSpec_JudgeModel(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("parses judge_model from YAML", func(t *testing.T) {
		yamlContent := `name: judge-test
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
  judge_model: claude-opus-4.6
`
		specPath := filepath.Join(tempDir, "judge.yaml")
		if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
			t.Fatalf("Failed to write spec file: %v", err)
		}

		spec, err := LoadBenchmarkSpec(specPath)
		if err != nil {
			t.Fatalf("Failed to load spec: %v", err)
		}

		if spec.Config.JudgeModel != "claude-opus-4.6" {
			t.Errorf("Expected judge_model='claude-opus-4.6', got '%s'", spec.Config.JudgeModel)
		}
		if spec.Config.ModelID != "gpt-4o" {
			t.Errorf("Expected model='gpt-4o', got '%s'", spec.Config.ModelID)
		}
	})

	t.Run("defaults to empty when omitted", func(t *testing.T) {
		yamlContent := `name: no-judge
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
`
		specPath := filepath.Join(tempDir, "no-judge.yaml")
		if err := os.WriteFile(specPath, []byte(yamlContent), 0644); err != nil {
			t.Fatalf("Failed to write spec file: %v", err)
		}

		spec, err := LoadBenchmarkSpec(specPath)
		if err != nil {
			t.Fatalf("Failed to load spec: %v", err)
		}

		if spec.Config.JudgeModel != "" {
			t.Errorf("Expected empty judge_model, got '%s'", spec.Config.JudgeModel)
		}
	})
}
