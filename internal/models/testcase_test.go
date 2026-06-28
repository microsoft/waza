package models

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestLoadTestCase_UnknownFieldIgnoredForCompatibility(t *testing.T) {
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
	tc, err := LoadTestCase(p)
	if err != nil {
		t.Fatalf("LoadTestCase returned error for same-major unknown field: %v", err)
	}
	if tc.TestID != "tc-bogus" {
		t.Errorf("TestID = %q, want tc-bogus", tc.TestID)
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

func TestResponderConfigParsesUnderInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	yaml := `
id: configure-agent
name: Configure a research agent
inputs:
  prompt: "Add a new agent to my application"
  responder:
    model: gpt-4o
    instructions: |
      The agent you want is research-agent with tools web_search.
      If you can't infer an answer, abstain.
    max_followups: 8
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	tc, err := LoadTestCase(path)
	require.NoError(t, err)
	require.NotNil(t, tc.Stimulus.Responder)
	require.Equal(t, "gpt-4o", tc.Stimulus.Responder.Model)
	require.Equal(t, 8, tc.Stimulus.Responder.MaxFollowups)
	require.Contains(t, tc.Stimulus.Responder.Instructions, "research-agent")
}

func TestResponderValidationRejectsMissingInstructions(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{MaxFollowups: 3},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "instructions")
}

func TestResponderValidationRejectsZeroMaxFollowups(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 0},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_followups")
}

func TestResponderValidationRejectsBothResponderAndFollowUps(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			FollowUps: []string{"next"},
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 2},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "follow_up_prompts")
	require.Contains(t, err.Error(), "responder")
}

func TestResponderValidationAcceptsValidConfig(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 2},
		},
	}
	require.NoError(t, tc.Validate())
}

// -- Checkpoint validation tests --

func TestLoadTestCase_CheckpointsParsed(t *testing.T) {
	yamlData := `id: tc-cp
name: Checkpoint loader
inputs:
  prompt: do work
  follow_up_prompts:
    - keep going
checkpoints:
  - after_turn: 1
    graders:
      - name: mid-check
        type: text
        config:
          contains:
            - hello
  - after_turn: 2
    on_failure: stop
    graders:
      - name: end-check
        type: text
        config:
          contains:
            - bye
`
	dir := t.TempDir()
	p := filepath.Join(dir, "tc.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yamlData), 0o644))

	tc, err := LoadTestCase(p)
	require.NoError(t, err)
	require.Len(t, tc.Checkpoints, 2)
	require.Equal(t, 1, tc.Checkpoints[0].AfterTurn)
	require.Equal(t, CheckpointContinue, tc.Checkpoints[0].EffectiveOnFailure())
	require.Equal(t, CheckpointStop, tc.Checkpoints[1].EffectiveOnFailure())
	require.Len(t, tc.Checkpoints[0].Graders, 1)
	require.Equal(t, "mid-check", tc.Checkpoints[0].Graders[0].Identifier)
}

func TestCheckpointValidation(t *testing.T) {
	makeTC := func(cps []Checkpoint) *TestCase {
		return &TestCase{
			TestID:      "t",
			DisplayName: "t",
			Stimulus: TaskStimulus{
				Message:   "go",
				FollowUps: []string{"again", "more"},
			},
			Checkpoints: cps,
		}
	}
	validGrader := ValidatorInline{
		Identifier: "g",
		Kind:       GraderKindText,
		Parameters: TextGraderParameters{Contains: []string{"hi"}},
	}

	t.Run("after_turn < 1 rejected", func(t *testing.T) {
		tc := makeTC([]Checkpoint{{AfterTurn: 0, Graders: []ValidatorInline{validGrader}}})
		require.ErrorContains(t, tc.Validate(), "after_turn")
	})

	t.Run("missing graders rejected", func(t *testing.T) {
		tc := makeTC([]Checkpoint{{AfterTurn: 1}})
		require.ErrorContains(t, tc.Validate(), "graders")
	})

	t.Run("duplicate after_turn rejected", func(t *testing.T) {
		tc := makeTC([]Checkpoint{
			{AfterTurn: 1, Graders: []ValidatorInline{validGrader}},
			{AfterTurn: 1, Graders: []ValidatorInline{validGrader}},
		})
		require.ErrorContains(t, tc.Validate(), "duplicate")
	})

	t.Run("after_turn exceeds turns rejected", func(t *testing.T) {
		// 1 initial + 2 follow-ups = 3 turns; 4 is out of range.
		tc := makeTC([]Checkpoint{{AfterTurn: 4, Graders: []ValidatorInline{validGrader}}})
		require.ErrorContains(t, tc.Validate(), "exceeds")
	})

	t.Run("invalid on_failure rejected", func(t *testing.T) {
		tc := makeTC([]Checkpoint{{AfterTurn: 1, OnFailure: "maybe", Graders: []ValidatorInline{validGrader}}})
		require.ErrorContains(t, tc.Validate(), "on_failure")
	})

	t.Run("valid case accepted", func(t *testing.T) {
		tc := makeTC([]Checkpoint{
			{AfterTurn: 1, Graders: []ValidatorInline{validGrader}},
			{AfterTurn: 3, OnFailure: CheckpointStop, Graders: []ValidatorInline{validGrader}},
		})
		require.NoError(t, tc.Validate())
	})

	t.Run("upper bound skipped for responder", func(t *testing.T) {
		// With responder, total turn count is unknown at validation time;
		// only a structural sanity check applies.
		tc := &TestCase{
			TestID:      "t",
			DisplayName: "t",
			Stimulus: TaskStimulus{
				Message:   "go",
				Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 4},
			},
			Checkpoints: []Checkpoint{{AfterTurn: 5, Graders: []ValidatorInline{validGrader}}},
		}
		require.NoError(t, tc.Validate())
	})
}
