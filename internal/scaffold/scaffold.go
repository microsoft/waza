// Package scaffold provides shared template functions for generating
// eval suites, task files, and fixtures used by waza new skill/eval.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/projectconfig"
)

// ValidateName rejects names with path-traversal characters or empty names.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("skill name must not be empty")
	}
	// Reject raw input containing path separators or traversal segments
	// before filepath.Clean can mask them (e.g. "a/.." cleans to ".").
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("skill name %q contains invalid path characters", name)
	}
	if name == "." || name == ".." || strings.Contains(name, "..") {
		return fmt.Errorf("skill name %q contains invalid path characters", name)
	}
	// Defense-in-depth: reject if Clean still produces traversal.
	cleaned := filepath.Clean(name)
	if cleaned == ".." || strings.Contains(cleaned, "/") || strings.Contains(cleaned, "\\") {
		return fmt.Errorf("skill name %q contains invalid path characters", name)
	}
	return nil
}

// TitleCase converts a kebab-case name to Title Case.
func TitleCase(s string) string {
	words := strings.Split(s, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// ReadProjectDefaults reads engine and model from .waza.yaml if it exists.
// Falls back to copilot-sdk and claude-sonnet-4.6.
func ReadProjectDefaults() (engine, model string) {
	dir, err := os.Getwd()
	if err != nil {
		return "copilot-sdk", "claude-sonnet-4.6"
	}
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		return "copilot-sdk", "claude-sonnet-4.6"
	}
	return cfg.Defaults.Engine, cfg.Defaults.Model
}

// EvalYAML returns a default eval.yaml template for the given skill name.
func EvalYAML(name, engine, model string) string {
	return fmt.Sprintf(`name: %s-eval
description: Evaluation suite for %s.
skill: %s
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: %s
  model: %s
metrics:
  - name: task_completion
    weight: 1.0
    threshold: 0.8
    description: Did the skill complete the assigned task?
graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 0"
  - type: text
    name: relevant_content
    config:
      regex_match:
        - "(?i)(explain|describe|analyze|implement)"
tasks:
  - "tasks/*.yaml"
`, name, name, name, engine, model)
}

// TaskFiles returns a map of task filename to content.
func TaskFiles(_ string) map[string]string {
	return map[string]string{
		"basic-usage.yaml":        basicUsageTask(),
		"edge-case.yaml":          edgeCaseTask(),
		"should-not-trigger.yaml": shouldNotTriggerTask(),
	}
}

// Fixture returns the default sample.py fixture content.
func Fixture() string {
	return `def hello(name):
    """Greet someone by name."""
    return f"Hello, {name}!"
`
}

func basicUsageTask() string {
	return `id: basic-usage-001
name: Basic Usage
description: |
  Test that the skill handles a typical request correctly.
tags:
  - basic
  - happy-path
inputs:
  prompt: "Help me with this task"
  files:
    - path: sample.py
expected:
  output_contains:
    - "function"
  outcomes:
    - type: task_completed
`
}

func edgeCaseTask() string {
	return `id: edge-case-001
name: Edge Case - Empty Input
description: |
  Test that the skill handles edge cases gracefully.
tags:
  - edge-case
inputs:
  prompt: ""
expected:
  outcomes:
    - type: task_completed
`
}

func shouldNotTriggerTask() string {
	return `id: should-not-trigger-001
name: Should Not Trigger
description: |
  Test that the skill does NOT activate on unrelated prompts.
  This validates trigger specificity.
tags:
  - anti-trigger
  - negative-test
inputs:
  prompt: "What is the weather today?"
expected:
  output_not_contains:
    - "skill activated"
`
}
