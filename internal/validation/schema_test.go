package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestCompileSchemaOnceDefersAndCachesCompilation(t *testing.T) {
	var invalid func() any
	require.NotPanics(t, func() {
		compiled := compileSchemaOnce("{", "invalid.json")
		invalid = func() any { return compiled() }
	})
	require.Panics(t, func() {
		invalid()
	})
	require.Panics(t, func() {
		invalid()
	})

	compiled := compileSchemaOnce(`{"type":"object"}`, "test.json")
	require.Same(t, compiled(), compiled())
}

func TestLazySchemasIndependentConcurrentValidation(t *testing.T) {
	originalEval, originalTask := evalSchema, taskSchema
	t.Cleanup(func() { evalSchema, taskSchema = originalEval, originalTask })

	for _, kind := range []string{"eval", "task"} {
		t.Run(kind, func(t *testing.T) {
			raw, valid, validate := schemas.EvalSchemaJSON, validEvalYAML, ValidateEvalBytes
			if kind == "task" {
				raw, valid, validate = schemas.TaskSchemaJSON, validTaskYAML, ValidateTaskBytes
			}
			compiled := compileSchemaOnce(raw, kind+".schema.json")
			const callers = 16
			seen := make(chan *jsonschema.Schema, callers)
			get := func() *jsonschema.Schema {
				schema := compiled()
				seen <- schema
				return schema
			}
			unused := compileSchemaOnce("{", "unused.schema.json")
			evalSchema, taskSchema = get, unused
			if kind == "task" {
				evalSchema, taskSchema = unused, get
			}

			start := make(chan struct{})
			results := make(chan []string, callers)
			var workers sync.WaitGroup
			for range callers {
				workers.Go(func() {
					<-start
					results <- validate([]byte(valid))
				})
			}
			close(start)
			workers.Wait()
			first := <-seen
			require.NotNil(t, first)
			for range callers {
				require.Empty(t, <-results)
			}
			for range callers - 1 {
				require.Same(t, first, <-seen)
			}
			require.Same(t, first, compiled())
			require.Empty(t, validate([]byte(valid)))
			require.Same(t, first, <-seen)
			require.Equal(t, []string{"/: got null, want object"}, validate([]byte("null\n")))
			require.Same(t, first, <-seen)
		})
	}
}

func TestTaskReasoningEffortSchemaAndExecutor(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "invalid"} {
			t.Run(fmt.Sprintf("checkpoint=%t/effort=%s", checkpoint, effort), func(t *testing.T) {
				graders := "graders:\n  - type: prompt\n    name: judge\n    config:\n      prompt: grade\n      reasoning_effort: " + effort + "\n"
				if checkpoint {
					graders = "checkpoints:\n  - after_turn: 1\n    " + strings.ReplaceAll(strings.TrimSpace(graders), "\n", "\n    ") + "\n"
				}
				task := validTaskYAML + graders
				errs := ValidateTaskBytes([]byte(task))
				if effort == "invalid" {
					require.NotEmpty(t, errs)
					return
				}
				require.Empty(t, errs)
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0o600))
				evalPath := filepath.Join(dir, "eval.yaml")
				eval := strings.Replace(validEvalYAML, "tasks/*.yaml", "task.yaml", 1)
				for _, executor := range []string{"mock", "copilot-sdk"} {
					require.NoError(t, os.WriteFile(evalPath, []byte(strings.Replace(eval, "executor: mock", "executor: "+executor, 1)), 0o600))
					evalErrs, taskErrs, err := ValidateEvalFile(evalPath)
					require.NoError(t, err)
					require.Empty(t, evalErrs)
					if executor == "mock" {
						require.Contains(t, strings.Join(taskErrs["task.yaml"], "\n"), "reasoning_effort requires executor copilot-sdk")
					} else {
						require.Empty(t, taskErrs)
					}
				}
			})
		}
	}
}

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

func TestValidateEvalBytes_GroupBy(t *testing.T) {
	for _, value := range []string{"", "model", "tags"} {
		t.Run(value, func(t *testing.T) {
			data := strings.Replace(validEvalYAML, "  model: gpt-4o", fmt.Sprintf("  model: gpt-4o\n  group_by: %q", value), 1)
			require.Empty(t, ValidateEvalBytes([]byte(data)))
		})
	}

	data := strings.Replace(validEvalYAML, "  model: gpt-4o", "  model: gpt-4o\n  group_by: task_id", 1)
	require.NotEmpty(t, ValidateEvalBytes([]byte(data)))
}

func TestValidateEvalBytes_EmptyCommandMocksRequireSupportedConfiguration(t *testing.T) {
	valid := `schemaVersion: "1.3"
name: command-mocks
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: test-model
command_mocks: []
tasks: ["tasks/*.yaml"]
metrics:
  - name: pass
    weight: 1
    threshold: 1`
	require.Empty(t, ValidateEvalBytes([]byte(valid)))
	for _, version := range []string{"1.03", "01.003", "1.10", "02.00", "10.0"} {
		t.Run(version, func(t *testing.T) {
			data := strings.Replace(valid, `schemaVersion: "1.3"`, `schemaVersion: "`+version+`"`, 1)
			require.Empty(t, ValidateEvalBytes([]byte(data)))
		})
	}
	for _, version := range []string{"1.002", "01.02", "0.99"} {
		t.Run(version, func(t *testing.T) {
			data := strings.Replace(valid, `schemaVersion: "1.3"`, `schemaVersion: "`+version+`"`, 1)
			require.NotEmpty(t, ValidateEvalBytes([]byte(data)))
		})
	}
	require.NotEmpty(t, ValidateEvalBytes([]byte(strings.Replace(valid, `schemaVersion: "1.3"`, `schemaVersion: "1.2"`, 1))))
	require.NotEmpty(t, ValidateEvalBytes([]byte(strings.Replace(valid, "executor: copilot-sdk", "executor: mock", 1))))
}

func TestValidateEvalBytes_ReasoningEffort(t *testing.T) {
	valid := `name: reasoning
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: gpt-5
  reasoning_effort: high
  judge_reasoning_effort: low
graders:
  - type: prompt
    name: judge
    config:
      prompt: grade
      reasoning_effort: medium
metrics:
  - name: score
    weight: 1
    threshold: 0.8
tasks: ["tasks/*.yaml"]`
	require.Empty(t, ValidateEvalBytes([]byte(valid)))
	require.NotEmpty(t, ValidateEvalBytes([]byte(strings.Replace(valid, "reasoning_effort: high", "reasoning_effort: invalid", 1))))
	require.NotEmpty(t, ValidateEvalBytes([]byte(strings.Replace(valid, "executor: copilot-sdk", "executor: mock", 1))))
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
  trigger_skill_routing: true
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

func TestValidateEvalBytes_GraderReasoningEffortRequiresCopilotSDK(t *testing.T) {
	yaml := `name: reasoning
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
  model: gpt-5
graders:
  - type: prompt
    name: judge
    config:
      prompt: grade
      reasoning_effort: medium
metrics:
  - name: score
    weight: 1
    threshold: 0.8
tasks: ["tasks/*.yaml"]`
	require.NotEmpty(t, ValidateEvalBytes([]byte(yaml)))
	require.Empty(t, ValidateEvalBytes([]byte(strings.Replace(yaml, "executor: mock", "executor: copilot-sdk", 1))))
}

func TestValidateEvalFile_NativeUserInput(t *testing.T) {
	legacy := "schemaVersion: \"1.4\"\nname: legacy\nskill: s\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 60\n  executor: copilot-sdk\n  model: m\nmetrics: [{name: m, weight: 1, threshold: 1}]\ntasks: [tasks/*.yaml]\n"
	scenario := "schemaVersion: \"2.0\"\nname: native\nscenario: native-questions\nversion: \"1.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 60\n  executor: copilot-sdk\n  model: m\n  native_user_input: true\nmetrics: [{name: m, weight: 1, threshold: 1}]\ntasks: [tasks/*.yaml]\n"
	scripted := "id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n  - question: \"Deploy.*\"\n    answer: staging\n"
	cases := []struct {
		name, eval, task string
		evalErr, taskErr string
	}{
		{"valid scenario opt-in", scenario, scripted, "", ""},
		{"legacy scripted answers", legacy, scripted, "", "requires config.native_user_input: true"},
		{"legacy empty script", legacy, "id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses: []\n", "", "requires config.native_user_input: true"},
		{"legacy opt-in", strings.Replace(legacy, "  model: m\n", "  model: m\n  native_user_input: true\n", 1), "id: t\nname: T\ninputs:\n  prompt: go\n", "requires a scenario eval with schemaVersion 2.0", ""},
		{"invalid question regex", scenario, "id: t\nname: T\ninputs:\n  prompt: go\nuser_input_responses:\n  - question: \"Deploy)|(?:Delete\"\n    answer: a\n", "", "question is not a valid regular expression"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			evalPath := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(evalPath, []byte(tc.eval), 0o600))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "t.yaml"), []byte(tc.task), 0o600))
			evalErrs, taskErrs, err := ValidateEvalFile(evalPath)
			require.NoError(t, err)
			if tc.evalErr == "" {
				require.Empty(t, evalErrs)
			} else {
				require.Contains(t, joinErrs(evalErrs), tc.evalErr)
			}
			if tc.taskErr == "" {
				require.Empty(t, taskErrs)
			} else {
				require.Contains(t, joinErrs(taskErrs[filepath.Join("tasks", "t.yaml")]), tc.taskErr)
			}
		})
	}
}

func TestValidateEvalBytes_NativeUserInputRequiresScenarioCopilot(t *testing.T) {
	scenario := "schemaVersion: \"2.0\"\nname: native\nscenario: native-questions\nversion: \"1.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 60\n  executor: copilot-sdk\n  model: m\n  native_user_input: true\nmetrics: [{name: m, weight: 1, threshold: 1}]\ntasks: [tasks/*.yaml]\n"
	require.Empty(t, ValidateEvalBytes([]byte(scenario)), "control: scenario copilot-sdk opt-in is valid")
	for name, eval := range map[string]string{
		"legacy version":   strings.Replace(strings.Replace(scenario, "schemaVersion: \"2.0\"", "schemaVersion: \"1.4\"", 1), "scenario: native-questions\n", "skill: s\n", 1),
		"missing version":  strings.Replace(strings.Replace(scenario, "schemaVersion: \"2.0\"\n", "", 1), "scenario: native-questions\n", "skill: s\n", 1),
		"mock executor":    strings.Replace(scenario, "executor: copilot-sdk", "executor: mock", 1),
		"default executor": strings.Replace(scenario, "  executor: copilot-sdk\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, ValidateEvalBytes([]byte(eval)))
		})
	}
	for name, eval := range map[string]string{
		"flag false in legacy eval": strings.Replace(strings.Replace(strings.Replace(scenario, "schemaVersion: \"2.0\"", "schemaVersion: \"1.4\"", 1), "scenario: native-questions\n", "skill: s\n", 1), "native_user_input: true", "native_user_input: false", 1),
		"flag omitted with mock":    strings.Replace(strings.Replace(scenario, "executor: copilot-sdk", "executor: mock", 1), "  native_user_input: true\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.Empty(t, ValidateEvalBytes([]byte(eval)))
		})
	}
}
