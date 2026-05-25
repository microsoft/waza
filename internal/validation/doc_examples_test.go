package validation

import (
	"fmt"
	"testing"
)

// TestDocExamples_EvalYAML validates YAML examples from eval-yaml.mdx against the schemas.
func TestDocExamples_EvalYAML(t *testing.T) {
	// Example 1: "eval.yaml Structure" — full eval spec (line ~13)
	evalStructure := `name: code-explainer-eval
description: Evaluation suite for code-explainer skill
skill: code-explainer
version: "1.0"

config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: mock
  model: claude-sonnet-4.6

metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8

graders:
  - type: text
    name: explains_concepts
    config:
      regex_match:
        - "(?i)(function|logic|parameter)"
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 100"

tasks:
  - "tasks/*.yaml"
`

	// Example 2: "Config Section" (line ~62)
	evalConfigSection := `name: config-example
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  workers: 0
  model: claude-sonnet-4.6
  judge_model: gpt-4o
  executor: mock
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`

	// Example 3: "Graders Section" (line ~97)
	evalGradersSection := `name: graders-example
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
graders:
  - type: text
    name: checks_logic
    weight: 2.0
    config:
      regex_match:
        - "(?i)(function|variable|parameter)"
  - type: code
    name: has_minimum_output
    config:
      assertions:
        - "len(output) > 100"
        - "'success' in output.lower()"
  - type: text
    name: mentions_key_concepts
    config:
      contains:
        - "algorithm"
        - "optimization"
tasks:
  - "tasks/*.yaml"
`

	// Example 4: "From Files" tasks (line ~140)
	evalFromFiles := `name: from-files-example
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
  - "tasks/basic/*.yaml"
  - "tasks/advanced.yaml"
`

	// Example 5: "Simple Validation" pattern
	evalSimpleValidation := `name: simple-validation
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
graders:
  - type: text
    name: format_check
    config:
      regex_match:
        - "^[A-Z].*\\.$"
tasks:
  - "tasks/format/*.yaml"
`

	// Example 6: "Multi-Criteria Scoring"
	evalMultiCriteria := `name: multi-criteria
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
graders:
  - type: code
    name: completeness
    config:
      assertions:
        - "len(output) > 500"
        - "'function' in output"
        - "'parameter' in output"
tasks:
  - "tasks/completeness/*.yaml"
`

	// Example 7: "Hooks"
	evalHooks := `name: hooks-example
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
hooks:
  before_run:
    - command: "npm install"
      working_directory: "./fixtures"
      error_on_fail: true
  after_run:
    - command: "bash cleanup.sh"
  before_task:
    - command: "echo Starting task"
  after_task:
    - command: "bash collect-metrics.sh"
tasks:
  - "tasks/*.yaml"
`

	// Example 8: "Template Variables"
	evalTemplateVars := `name: template-vars-example
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock
  model: gpt-4o
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
inputs:
  language: python
  framework: fastapi
tasks:
  - "tasks/scaffold/*.yaml"
`

	// Example 9: "External Task Lists"
	evalExternalTasks := `name: shared-eval
skill: test-skill
version: "1.0"
tasks_from: shared-tasks.yaml
config:
  trials_per_task: 3
  timeout_seconds: 300
  executor: mock
  model: claude-sonnet-4.6
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`

	evalExamples := map[string]string{
		"eval structure":      evalStructure,
		"config section":      evalConfigSection,
		"graders section":     evalGradersSection,
		"from files":          evalFromFiles,
		"simple validation":   evalSimpleValidation,
		"multi-criteria":      evalMultiCriteria,
		"hooks":               evalHooks,
		"template variables":  evalTemplateVars,
		"external task lists": evalExternalTasks,
	}

	for name, yaml := range evalExamples {
		t.Run(fmt.Sprintf("eval/%s", name), func(t *testing.T) {
			errs := ValidateEvalBytes([]byte(yaml))
			if len(errs) > 0 {
				for _, e := range errs {
					t.Errorf("  %s", e)
				}
			}
		})
	}
}

func TestDocExamples_TaskYAML(t *testing.T) {
	// Task file example (line ~150)
	taskBasicUsage := `id: basic-usage-001
name: Basic Usage - Python Function
description: Test that the skill explains a simple Python function correctly.

tags:
  - basic
  - happy-path

inputs:
  prompt: "Explain this function"
  files:
    - path: sample.py

expected:
  output_contains:
    - "function"
    - "parameter"
    - "return"
  outcomes:
    - type: task_completed
  behavior:
    max_tool_calls: 5
`

	// Behavioral constraints task example
	taskBehavioral := `id: efficient-001
name: Efficiency test
inputs:
  prompt: "Refactor this code"
expected:
  behavior:
    max_tool_calls: 3
    max_tokens: 1000
`

	taskExamples := map[string]string{
		"basic usage": taskBasicUsage,
		"behavioral":  taskBehavioral,
	}

	for name, yaml := range taskExamples {
		t.Run(fmt.Sprintf("task/%s", name), func(t *testing.T) {
			errs := ValidateTaskBytes([]byte(yaml))
			if len(errs) > 0 {
				for _, e := range errs {
					t.Errorf("  %s", e)
				}
			}
		})
	}
}
