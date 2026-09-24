package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const validEvalYAML = `name: test-eval
description: Test evaluation
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`

const invalidEvalYAML = `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: invalid-engine
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 1.5
tasks:
  - "tasks/*.yaml"
`

const validTaskYAML = `id: task-1
name: Basic task
inputs:
  prompt: "Explain this code"
`

const invalidTaskYAML = `name: Missing ID task
description: This task is missing the required id field
`

func TestValidateEvalBytes_Valid(t *testing.T) {
	errs := ValidateEvalBytes([]byte(validEvalYAML))
	require.Empty(t, errs, "valid eval should have no errors")
}

func TestValidateEvalBytes_InstructionFiles(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
  instruction_files:
    - .github/instructions/project.instructions.md
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.Empty(t, errs, "eval with instruction_files should have no errors")
}

func TestValidateEvalBytes_InjectSkillBody(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
  inject_skill_body: false
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.Empty(t, errs, "eval with inject_skill_body should have no errors")
}

func TestValidateEvalBytes_ToolConstraintArgs(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
graders:
  - name: scoped-tools
    type: tool_constraint
    config:
      expect_tools:
        - tool: bash
          args:
            command:
              equals: "go test ./..."
      reject_tools:
        - tool: bash
          args:
            timeout:
              range:
                gt: 300
      allow_only:
        - tool: bash
          args:
            command:
              regex: "^go test"
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.Empty(t, errs, "eval tool specs should accept structured args matchers")
}

func TestValidateEvalBytes_ToolConstraintRejectsEmptyArgMatcher(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
graders:
  - name: scoped-tools
    type: tool_constraint
    config:
      allow_only:
        - tool: bash
          args:
            command:
              regex: ""
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.NotEmpty(t, errs, "empty argument matchers should fail schema validation")
}

func TestValidateEvalBytes_RemoteGraderRefWithoutType(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
graders:
  - ref: github.com/waza-evals/fact#factuality@v1.0.0
    name: factuality_strict
    weight: 2
    config:
      threshold: 0.9
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.Empty(t, errs, "eval with remote grader ref should have no schema errors")
}

func TestValidateEvalBytes_Invalid(t *testing.T) {
	errs := ValidateEvalBytes([]byte(invalidEvalYAML))
	require.NotEmpty(t, errs, "invalid eval should have errors")

	joined := joinErrs(errs)
	require.Contains(t, joined, "executor")
	require.Contains(t, joined, "threshold")
}

func TestValidateEvalBytes_SandboxRequiresCopilotSDK(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
schemaVersion: "1.3"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-4o
  sandbox:
    enabled: true
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.NotEmpty(t, errs)
	require.Contains(t, joinErrs(errs), "copilot-sdk")
}

func TestValidateEvalBytes_SandboxRequiresSchemaVersion13(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
schemaVersion: "1.2"
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: gpt-4o
  sandbox:
    enabled: true
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.NotEmpty(t, errs)
	require.Contains(t, joinErrs(errs), "1.3")
}

func TestValidateEvalBytes_SandboxAcceptsSchemaVersion13OrNewer(t *testing.T) {
	for _, version := range []string{"", "1.3", "1.10"} {
		t.Run(version, func(t *testing.T) {
			versionLine := ""
			if version != "" {
				versionLine = fmt.Sprintf("schemaVersion: %q\n", version)
			}
			yaml := fmt.Sprintf(`name: test-eval
skill: test-skill
%sconfig:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: gpt-4o
  sandbox:
    enabled: true
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`, versionLine)
			errs := ValidateEvalBytes([]byte(yaml))
			require.Empty(t, errs)
		})
	}
}

func TestValidateEvalBytes_SandboxRequiresExplicitExecutor(t *testing.T) {
	yaml := `name: test-eval
skill: test-skill
schemaVersion: "1.3"
config:
  trials_per_task: 1
  timeout_seconds: 60
  model: gpt-4o
  sandbox:
    enabled: true
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	errs := ValidateEvalBytes([]byte(yaml))
	require.NotEmpty(t, errs)
	require.Contains(t, joinErrs(errs), "executor")
}

func TestValidateTaskBytes_Valid(t *testing.T) {
	errs := ValidateTaskBytes([]byte(validTaskYAML))
	require.Empty(t, errs, "valid task should have no errors")
}

func TestValidateTaskBytes_InstructionFiles(t *testing.T) {
	yaml := `id: task-1
name: Basic task
instruction_files:
  - .github/instructions/task.instructions.md
inputs:
  prompt: "Explain this code"
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.Empty(t, errs, "task with instruction_files should have no errors")
}

func TestValidateTaskBytes_ToolConstraintArgs(t *testing.T) {
	yaml := `id: task-1
name: Scoped tools
inputs:
  prompt: "Run tests"
graders:
  - name: scoped-tools
    type: tool_constraint
    config:
      expect_tools:
        - tool: bash
          args:
            command:
              regex: "^go test"
      reject_tools:
        - tool: bash
          args:
            command:
              equals: "rm -rf /"
      allow_only:
        - tool: bash
          args:
            command:
              contains: "go test"
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.Empty(t, errs, "task tool specs should accept structured args matchers")
}

func TestValidateTaskBytes_ToolConstraintRejectsEmptyArgMatcher(t *testing.T) {
	yaml := `id: task-1
name: Scoped tools
inputs:
  prompt: "Run tests"
graders:
  - name: scoped-tools
    type: tool_constraint
    config:
      reject_tools:
        - tool: bash
          args:
            command:
              contains: ""
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.NotEmpty(t, errs, "empty argument matchers should fail schema validation")
}

func TestValidateTaskBytes_Responder(t *testing.T) {
	yaml := `id: task-1
name: Configure agent
inputs:
  prompt: "add agent"
  responder:
    instructions: "be research-agent; abstain if unknown"
    max_followups: 8
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.Empty(t, errs, "task with inputs.responder should have no errors")
}

func TestValidateTaskBytes_FollowUpPrompts(t *testing.T) {
	yaml := `id: task-1
name: Multi-turn task
inputs:
  prompt: "start"
  follow_up_prompts:
    - "and then?"
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.Empty(t, errs, "task with inputs.follow_up_prompts should have no errors")
}

func TestValidateTaskBytes_ResponderAndFollowUpsMutuallyExclusive(t *testing.T) {
	yaml := `id: task-1
name: Conflicting task
inputs:
  prompt: "start"
  follow_up_prompts:
    - "and then?"
  responder:
    instructions: "be research-agent"
    max_followups: 3
`
	errs := ValidateTaskBytes([]byte(yaml))
	require.NotEmpty(t, errs, "responder and follow_up_prompts together should be rejected")
}

func TestValidateTaskBytes_Invalid(t *testing.T) {
	errs := ValidateTaskBytes([]byte(invalidTaskYAML))
	require.NotEmpty(t, errs, "invalid task should have errors")

	joined := joinErrs(errs)
	require.Contains(t, joined, "id")
}

func TestValidateEvalFile_Valid(t *testing.T) {
	dir := t.TempDir()

	// Write eval.yaml
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(validEvalYAML), 0644))

	// Create tasks directory with a valid task
	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "basic.yaml"), []byte(validTaskYAML), 0644))

	evalErrs, taskErrs, err := ValidateEvalFile(evalPath)
	require.NoError(t, err)
	require.Empty(t, evalErrs, "valid eval file should have no errors")
	require.Empty(t, taskErrs, "valid tasks should have no errors")
}

func TestValidateEvalFile_InvalidEval(t *testing.T) {
	dir := t.TempDir()

	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(invalidEvalYAML), 0644))

	evalErrs, _, err := ValidateEvalFile(evalPath)
	require.NoError(t, err)
	require.NotEmpty(t, evalErrs, "invalid eval should return errors")
}

func TestValidateEvalFile_CustomAgentExample(t *testing.T) {
	evalPath := filepath.Join("..", "..", "examples", "custom-agent", "eval.yaml")

	evalErrs, taskErrs, err := ValidateEvalFile(evalPath)
	require.NoError(t, err)
	require.Empty(t, evalErrs, "custom-agent example eval should be valid")
	require.Empty(t, taskErrs, "custom-agent example tasks should be valid")
}

func TestValidateEvalFile_InvalidTask(t *testing.T) {
	dir := t.TempDir()

	// Write valid eval.yaml
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(validEvalYAML), 0644))

	// Create tasks directory with invalid task
	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "bad.yaml"), []byte(invalidTaskYAML), 0644))

	evalErrs, taskErrs, err := ValidateEvalFile(evalPath)
	require.NoError(t, err)
	require.Empty(t, evalErrs, "eval itself is valid")
	require.NotEmpty(t, taskErrs, "should have task errors")

	badErrs, ok := taskErrs[filepath.Join("tasks", "bad.yaml")]
	require.True(t, ok, "should have errors for bad.yaml")
	require.NotEmpty(t, badErrs)
}

func TestValidateEvalFile_NotFound(t *testing.T) {
	_, _, err := ValidateEvalFile("/nonexistent/eval.yaml")
	require.Error(t, err)
}

func joinErrs(errs []string) string {
	result := ""
	for _, e := range errs {
		result += e + "\n"
	}
	return result
}
