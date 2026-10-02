package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEvalSpecCommandMocks(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "eval.yaml")
	data := `schemaVersion: "1.3"
name: cli-eval
skill: cli-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: test-model
command_mocks:
  - name: az
    expect_calls: 2
    responses:
      - args: [account, show]
        stdout: {name: Test Subscription}
      - args_regex: [group, "show", ".+"]
        fixture: fixtures/group.json
        exit_code: 0
tasks: [tasks/*.yaml]
metrics:
  - name: pass
    weight: 1
    threshold: 1
`
	if err := os.WriteFile(specPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	spec, err := LoadEvalSpec(specPath)
	if err != nil {
		t.Fatalf("LoadEvalSpec() error = %v", err)
	}
	if len(spec.CommandMocks) != 1 || spec.CommandMocks[0].Name != "az" {
		t.Fatalf("unexpected command mocks: %+v", spec.CommandMocks)
	}
	if spec.CommandMocks[0].ExpectCalls == nil || *spec.CommandMocks[0].ExpectCalls != 2 {
		t.Fatalf("expect_calls = %v, want 2", spec.CommandMocks[0].ExpectCalls)
	}
}

func TestLoadEvalSpecCommandMocksRequireSchemaVersionAndCopilot(t *testing.T) {
	base := `schemaVersion: "%s"
name: cli-eval
skill: cli-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: %s
  model: test-model
command_mocks:
  - name: az
    responses:
      - args: []
tasks: [tasks/*.yaml]
metrics:
  - name: pass
    weight: 1
    threshold: 1
`
	for _, test := range []struct {
		name, version, executor, wantError string
	}{
		{"old schema", "1.2", "copilot-sdk", "command_mocks requires schemaVersion 1.3"},
		{"mock executor", "1.3", "mock", "command_mocks requires executor copilot-sdk"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "eval.yaml")
			data := strings.ReplaceAll(base, "%s", test.version)
			// The first replacement is the schema version; fill the executor separately.
			data = strings.Replace(data, "executor: "+test.version, "executor: "+test.executor, 1)
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadEvalSpec(path)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("LoadEvalSpec() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestLoadTestCaseCommandMocksOverrideAndValidate(t *testing.T) {
	taskPath := filepath.Join(t.TempDir(), "task.yaml")
	data := `id: cli-task
name: CLI task
inputs:
  prompt: Run az.
command_mocks: []
`
	if err := os.WriteFile(taskPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := LoadTestCase(taskPath)
	if err != nil {
		t.Fatalf("LoadTestCase() error = %v", err)
	}
	if task.CommandMocks == nil || len(*task.CommandMocks) != 0 {
		t.Fatalf("empty task override was not preserved: %#v", task.CommandMocks)
	}

	data = strings.Replace(data, "command_mocks: []", `command_mocks:
  - name: az
    responses:
      - args_regex: ["["]`, 1)
	if err := os.WriteFile(taskPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTestCase(taskPath); err == nil || !strings.Contains(err.Error(), "args_regex[0]") {
		t.Fatalf("expected invalid regex error, got %v", err)
	}
}

func TestValidateCommandMocksRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name  string
		mock  CommandMockConfig
		error string
	}{
		{
			name:  "missing matcher",
			mock:  CommandMockConfig{Name: "az", Responses: []CommandMockResponse{{}}},
			error: "exactly one of args or args_regex",
		},
		{
			name: "both matchers",
			mock: CommandMockConfig{Name: "az", Responses: []CommandMockResponse{{
				Args: []string{}, ArgsRegex: []string{},
			}}},
			error: "exactly one of args or args_regex",
		},
		{
			name: "unsafe command name",
			mock: CommandMockConfig{Name: "../az", Responses: []CommandMockResponse{{
				Args: []string{},
			}}},
			error: "executable name",
		},
		{
			name: "bad fixture path",
			mock: CommandMockConfig{Name: "az", Responses: []CommandMockResponse{{
				Args: []string{}, Fixture: "../secret.json",
			}}},
			error: "fixture must be a relative path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateCommandMocks([]CommandMockConfig{test.mock}); err == nil || !strings.Contains(err.Error(), test.error) {
				t.Fatalf("ValidateCommandMocks() error = %v, want %q", err, test.error)
			}
		})
	}
}

func TestValidateCommandMocksSchemaVersion(t *testing.T) {
	for _, test := range []struct {
		version  string
		wantFail bool
	}{
		{version: "1.2", wantFail: true},
		{version: "1.3"},
		{version: "1.10"},
		{version: "2.0"},
	} {
		t.Run(test.version, func(t *testing.T) {
			err := ValidateCommandMocksSchemaVersion(test.version)
			if test.wantFail && err == nil {
				t.Fatal("expected version rejection")
			}
			if !test.wantFail && err != nil {
				t.Fatalf("unexpected version rejection: %v", err)
			}
		})
	}
}
