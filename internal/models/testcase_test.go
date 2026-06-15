package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTestCase_ShouldTriggerField(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantNil bool
		wantVal bool
	}{
		{
			name: "should_trigger true",
			yaml: `id: tc-trigger-true
name: Trigger True
inputs:
  prompt: "test prompt"
expected:
  should_trigger: true
`,
			wantNil: false,
			wantVal: true,
		},
		{
			name: "should_trigger false",
			yaml: `id: tc-trigger-false
name: Trigger False
inputs:
  prompt: "test prompt"
expected:
  should_trigger: false
`,
			wantNil: false,
			wantVal: false,
		},
		{
			name: "should_trigger omitted",
			yaml: `id: tc-trigger-omit
name: Trigger Omitted
inputs:
  prompt: "test prompt"
`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "tc.yaml")
			if err := os.WriteFile(p, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			tc, err := LoadTestCase(p)
			if err != nil {
				t.Fatalf("LoadTestCase: %v", err)
			}

			if tt.wantNil {
				if tc.Expectation.ExpectedTrigger != nil {
					t.Errorf("expected ExpectedTrigger nil, got %v", *tc.Expectation.ExpectedTrigger)
				}
				return
			}

			if tc.Expectation.ExpectedTrigger == nil {
				t.Fatal("expected ExpectedTrigger non-nil, got nil")
			}
			if *tc.Expectation.ExpectedTrigger != tt.wantVal {
				t.Errorf("ExpectedTrigger = %v, want %v", *tc.Expectation.ExpectedTrigger, tt.wantVal)
			}
		})
	}
}

func TestLoadTestCase_UnknownFieldRejected(t *testing.T) {
	yamlData := `id: tc-bogus
name: has bogus field
bogus_field: true
inputs:
  prompt: do something
expected:
  output_contains:
    - "hello"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlData), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := LoadTestCase(p)
	if err == nil {
		t.Fatal("expected error for unknown field 'bogus_field', got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error should mention bogus_field, got: %v", err)
	}
}

func TestLoadTestCase_InvalidTimeoutRejected(t *testing.T) {
	yamlData := `id: tc-invalid-timeout
name: invalid timeout
timeout_seconds: 0
inputs:
  prompt: do something
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlData), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := LoadTestCase(p)
	if err == nil {
		t.Fatal("expected error for timeout_seconds: 0, got nil")
	}
	if !strings.Contains(err.Error(), `test case "tc-invalid-timeout" timeout_seconds must be at least 1, got 0`) {
		t.Errorf("error should describe invalid timeout_seconds, got: %v", err)
	}
}

func TestLoadTestCase_NegativeFirstEventTimeoutRejected(t *testing.T) {
	yamlData := `id: tc-bad-first-event
name: bad first event
first_event_timeout_seconds: -1
inputs:
  prompt: do something
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlData), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := LoadTestCase(p)
	if err == nil {
		t.Fatal("expected error for first_event_timeout_seconds: -1, got nil")
	}
	if !strings.Contains(err.Error(), `test case "tc-bad-first-event" first_event_timeout_seconds must not be negative, got -1`) {
		t.Errorf("error should describe invalid first_event_timeout_seconds, got: %v", err)
	}
}

func TestLoadTestCase_ZeroFirstEventTimeoutAllowed(t *testing.T) {
	// Unlike timeout_seconds, 0 is valid for first_event_timeout_seconds — it
	// disables the first-event watchdog for the task.
	yamlData := `id: tc-zero-first-event
name: zero first event
first_event_timeout_seconds: 0
inputs:
  prompt: do something
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlData), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tc, err := LoadTestCase(p)
	if err != nil {
		t.Fatalf("expected first_event_timeout_seconds: 0 to be accepted, got: %v", err)
	}
	if tc.FirstEventTimeoutSec == nil || *tc.FirstEventTimeoutSec != 0 {
		t.Errorf("expected FirstEventTimeoutSec == 0, got %v", tc.FirstEventTimeoutSec)
	}
}

func TestLoadTestCase_OutputContainsAny(t *testing.T) {
	yamlData := `id: tc-may
name: test may-include
inputs:
  prompt: do something
expected:
  output_contains_any:
    - "option_a"
    - "option_b"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlData), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tc, err := LoadTestCase(p)
	if err != nil {
		t.Fatalf("LoadTestCase: %v", err)
	}
	if len(tc.Expectation.MayInclude) != 2 {
		t.Fatalf("expected 2 MayInclude entries, got %d", len(tc.Expectation.MayInclude))
	}
	if tc.Expectation.MayInclude[0] != "option_a" {
		t.Errorf("expected first entry 'option_a', got %q", tc.Expectation.MayInclude[0])
	}
}

// TestLoadTestCase_FollowUpPrompts was removed: it referenced
// TaskStimulus.FollowUps which does not exist. Re-add when that
// field is implemented.

func TestLoadTestCase_SkillDirectories(t *testing.T) {
	yamlContent := `id: skill-test
name: Skill directories test
inputs:
  prompt: "test prompt"
skill_directories:
  - ./skills/custom
  - /absolute/skills
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tc, err := LoadTestCase(p)
	if err != nil {
		t.Fatalf("LoadTestCase: %v", err)
	}
	if len(tc.SkillPaths) != 2 {
		t.Fatalf("Expected 2 skill paths, got %d", len(tc.SkillPaths))
	}
	if tc.SkillPaths[0] != "./skills/custom" {
		t.Errorf("Expected first skill path './skills/custom', got %q", tc.SkillPaths[0])
	}
	if tc.SkillPaths[1] != "/absolute/skills" {
		t.Errorf("Expected second skill path '/absolute/skills', got %q", tc.SkillPaths[1])
	}
}

func TestLoadTestCase_InstructionFiles(t *testing.T) {
	yamlContent := `id: instructions-test
name: Instruction files test
inputs:
  prompt: "test prompt"
instruction_files:
  - .github/instructions/task.instructions.md
  - docs/task.instructions.md
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	if err := os.WriteFile(p, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tc, err := LoadTestCase(p)
	if err != nil {
		t.Fatalf("LoadTestCase: %v", err)
	}
	if len(tc.InstructionFiles) != 2 {
		t.Fatalf("Expected 2 instruction files, got %d", len(tc.InstructionFiles))
	}
	if tc.InstructionFiles[0] != ".github/instructions/task.instructions.md" {
		t.Errorf("Expected first instruction file '.github/instructions/task.instructions.md', got %q", tc.InstructionFiles[0])
	}
	if tc.InstructionFiles[1] != "docs/task.instructions.md" {
		t.Errorf("Expected second instruction file 'docs/task.instructions.md', got %q", tc.InstructionFiles[1])
	}
}
