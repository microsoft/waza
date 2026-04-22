// Copyright 2024 Microsoft Corporation. All rights reserved.
// Use of this source code is governed by the MIT License that can be found in the LICENSE file.

package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraderConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		specYAML    string
		expectError bool
		errorMsg    string
	}{
		{
			name: "code grader with assertions at wrong level",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "code"
    # This should be under config, not at root level
    assertions:
      - "hello"
`,
			expectError: true,
			errorMsg:    "field assertions not found",
		},
		{
			name: "code grader with no assertions at all",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "code"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one assertion",
		},
		{
			name: "code grader with correct config",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "code"
    config:
      assertions:
        - "hello"
`,
			expectError: false,
		},
		{
			name: "diff grader with no expected_files",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "diff"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one file",
		},
		{
			name: "diff grader with expected_files",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "diff"
    config:
      expected_files:
        - path: "test.txt"
          snapshot: "expected.txt"
`,
			expectError: false,
		},
		{
			name: "json_schema grader with no schema",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "json_schema"
    config: {}
`,
			expectError: true,
			errorMsg:    "must specify either config.schema or config.schema_file",
		},
		{
			name: "program grader with no command",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "program"
    config: {}
`,
			expectError: true,
			errorMsg:    "must specify config.command",
		},
		{
			name: "trigger grader with no skill_path",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "trigger"
    config:
      mode: "precision"
`,
			expectError: true,
			errorMsg:    "must specify config.skill_path",
		},
		{
			name: "action_sequence grader with no expected_actions",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "action_sequence"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one action",
		},
		{
			name: "skill_invocation grader with no required_skills",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "skill_invocation"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one skill",
		},
		{
			name: "tool_constraint grader with no expect_tools or reject_tools",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "tool_constraint"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one tool",
		},
		{
			name: "file grader with no criteria",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "file"
    config: {}
`,
			expectError: true,
			errorMsg:    "must specify at least one",
		},
		{
			name: "text grader with empty config is allowed",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "text"
    config: {}
`,
			expectError: false,
		},
		{
			name: "behavior grader with empty config is allowed",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "behavior"
    config: {}
`,
			expectError: false,
		},
		{
			name: "prompt grader with empty config is allowed",
			specYAML: `name: test
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: test-model
graders:
  - name: "my grader"
    type: "prompt"
    config: {}
`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			specPath := filepath.Join(tempDir, "spec.yaml")
			if err := os.WriteFile(specPath, []byte(tt.specYAML), 0644); err != nil {
				t.Fatalf("Failed to write spec file: %v", err)
			}

			_, err := LoadEvalSpec(specPath)
			if (err != nil) != tt.expectError {
				t.Errorf("LoadEvalSpec() error = %v, expectError %v", err, tt.expectError)
			}

			if tt.expectError && err != nil && tt.errorMsg != "" {
				if !strings.Contains(err.Error(), tt.errorMsg) && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg)) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorMsg, err)
				}
			}
		})
	}
}

func TestValidatorInline_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		taskYAML    string
		expectError bool
		errorMsg    string
	}{
		{
			name: "code validator with no assertions",
			taskYAML: `id: test-001
name: Test Case
inputs:
  prompt: Test prompt
graders:
  - name: "my validator"
    type: "code"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one assertion",
		},
		{
			name: "code validator with correct config",
			taskYAML: `id: test-001
name: Test Case
inputs:
  prompt: Test prompt
graders:
  - name: "my validator"
    type: "code"
    config:
      assertions:
        - "result == 42"
`,
			expectError: false,
		},
		{
			name: "diff validator with no expected_files",
			taskYAML: `id: test-001
name: Test Case
inputs:
  prompt: Test prompt
graders:
  - name: "my validator"
    type: "diff"
    config: {}
`,
			expectError: true,
			errorMsg:    "must have at least one file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			taskPath := filepath.Join(tempDir, "task.yaml")
			if err := os.WriteFile(taskPath, []byte(tt.taskYAML), 0644); err != nil {
				t.Fatalf("Failed to write task file: %v", err)
			}

			_, err := LoadTestCase(taskPath)
			if (err != nil) != tt.expectError {
				t.Errorf("LoadTestCase() error = %v, expectError %v", err, tt.expectError)
			}

			if tt.expectError && err != nil && tt.errorMsg != "" {
				if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errorMsg, err)
				}
			}
		})
	}
}
