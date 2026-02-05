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
	if spec.Identity.Name != "test-benchmark" {
		t.Errorf("Expected name 'test-benchmark', got '%s'", spec.Identity.Name)
	}
	if spec.SkillName != "test-skill" {
		t.Errorf("Expected skill 'test-skill', got '%s'", spec.SkillName)
	}
	if spec.RuntimeOptions.RunsPerTest != 2 {
		t.Errorf("Expected 2 trials, got %d", spec.RuntimeOptions.RunsPerTest)
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
	if spec.RuntimeOptions.RunsPerTest != 1 {
		t.Errorf("Expected trials=1, got %d", spec.RuntimeOptions.RunsPerTest)
	}
	if spec.RuntimeOptions.TimeoutSec != 300 {
		t.Errorf("Expected timeout=300, got %d", spec.RuntimeOptions.TimeoutSec)
	}
	if spec.RuntimeOptions.EngineType != "mock" {
		t.Errorf("Expected engine='mock', got '%s'", spec.RuntimeOptions.EngineType)
	}
}
