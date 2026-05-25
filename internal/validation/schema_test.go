package validation

import (
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

func TestValidateEvalBytes_Invalid(t *testing.T) {
	errs := ValidateEvalBytes([]byte(invalidEvalYAML))
	require.NotEmpty(t, errs, "invalid eval should have errors")

	joined := joinErrs(errs)
	require.Contains(t, joined, "executor")
	require.Contains(t, joined, "threshold")
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
