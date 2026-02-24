package suggest

import (
	"fmt"
	"strings"
)

const evalYAMLSchemaSummary = `Top-level eval.yaml fields:
- name (string)
- description (string)
- skill (string)
- version (string)
- config:
  - trials_per_task (int >= 1)
  - timeout_seconds (int >= 1)
  - parallel (bool)
  - executor (mock|copilot-sdk)
  - model (string)
- graders[]: Each entry MUST be an object with "type" and "name" fields (never a bare string).
  - type (code|prompt|regex|file|keyword|json_schema|program|behavior|action_sequence|skill_invocation|diff|tool_constraint)
  - name (string, required)
  - config (map, required fields depend on type — see grader documentation below)
- metrics[]:
  - name (string)
  - weight (float)
  - threshold (float)
- tasks[] (glob patterns, usually "tasks/*.yaml")`

const exampleEvalYAML = `name: example-skill-eval
description: Evaluation suite for example-skill
skill: example-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: copilot-sdk
  model: claude-opus-4.6
graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 0"
  - type: regex
    name: no_errors
    config:
      must_not_match:
        - "(?i)error|exception"
  - type: skill_invocation
    name: skill_was_invoked
    config:
      required_skills:
        - example-skill
      mode: any_order
metrics:
  - name: task_completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"`

type promptData struct {
	SkillName      string
	Description    string
	Triggers       string
	AntiTriggers   string
	ContentSummary string
	GraderTypes    string
	SkillContent   string
}

// renderSelectionPrompt builds the pass-1 prompt that asks the LLM
// to choose appropriate grader types for the skill.
func renderSelectionPrompt(data promptData) string {
	var b strings.Builder
	b.WriteString("You are selecting grader types for a waza evaluation suite.\n")
	b.WriteString("Given the skill description below, choose which grader types are most appropriate.\n\n")
	b.WriteString("Return ONLY a YAML list of grader type names, one per line, like:\n")
	b.WriteString("```yaml\ngraders:\n  - code\n  - keyword\n  - skill_invocation\n```\n\n")
	b.WriteString("Choose 2-5 grader types that best validate this skill's behavior.\n")
	b.WriteString("Consider: does the skill produce text output? files? invoke other skills? need format checks?\n\n")
	b.WriteString("Skill metadata:\n")
	fmt.Fprintf(&b, "- Name: %s\n", data.SkillName)
	fmt.Fprintf(&b, "- Description: %s\n", data.Description)
	fmt.Fprintf(&b, "- Triggers (USE FOR): %s\n", data.Triggers)
	fmt.Fprintf(&b, "- Anti-triggers (DO NOT USE FOR): %s\n", data.AntiTriggers)
	fmt.Fprintf(&b, "- Content summary: %s\n\n", data.ContentSummary)
	b.WriteString("Available grader types:\n")
	b.WriteString(GraderSummaries())
	b.WriteString("\n")
	return b.String()
}

// renderImplementationPrompt builds the pass-2 prompt that generates
// the full eval YAML, with detailed docs for the selected grader types.
func renderImplementationPrompt(data promptData, graderDocs string) string {
	var b strings.Builder
	b.WriteString("You are generating a waza evaluation suite for a skill.\n")
	b.WriteString("Return ONLY YAML in this exact schema:\n\n")
	b.WriteString("eval_yaml: |\n")
	b.WriteString("  <full eval.yaml content>\n")
	b.WriteString("tasks:\n")
	b.WriteString("  - path: tasks/<task-file>.yaml\n")
	b.WriteString("    content: |\n")
	b.WriteString("      <task yaml>\n")
	b.WriteString("fixtures:\n")
	b.WriteString("  - path: fixtures/<fixture-file>\n")
	b.WriteString("    content: |\n")
	b.WriteString("      <fixture content>\n\n")
	b.WriteString("Requirements:\n")
	b.WriteString("- Ensure eval_yaml is valid waza BenchmarkSpec YAML.\n")
	b.WriteString("- Each grader entry MUST be an object with at least 'type' and 'name' fields. NEVER use bare strings like '- grader_name'. Always use '- name: grader_name' with a 'type' field.\n")
	b.WriteString("- Include required config fields for each grader type (see grader documentation below).\n")
	b.WriteString("- Include at least 3 diverse tasks and at least 1 negative/anti-trigger task.\n")
	b.WriteString("- Use grader types from the allowed list only.\n")
	b.WriteString("- Keep task IDs deterministic and kebab-case.\n")
	b.WriteString("- Make fixtures minimal and realistic for the tasks.\n\n")
	b.WriteString("Skill metadata:\n")
	fmt.Fprintf(&b, "- Name: %s\n", data.SkillName)
	fmt.Fprintf(&b, "- Description: %s\n", data.Description)
	fmt.Fprintf(&b, "- Triggers (USE FOR): %s\n", data.Triggers)
	fmt.Fprintf(&b, "- Anti-triggers (DO NOT USE FOR): %s\n", data.AntiTriggers)
	fmt.Fprintf(&b, "- Content summary: %s\n\n", data.ContentSummary)
	b.WriteString("waza eval YAML schema summary:\n")
	b.WriteString(evalYAMLSchemaSummary)
	b.WriteString("\n\n")
	b.WriteString("Example eval.yaml:\n")
	b.WriteString(exampleEvalYAML)
	b.WriteString("\n\n")
	if graderDocs != "" {
		b.WriteString("Grader documentation for the types you should use:\n")
		b.WriteString(graderDocs)
		b.WriteString("\n\n")
	}
	b.WriteString("Skill content (SKILL.md):\n")
	b.WriteString(data.SkillContent)
	b.WriteString("\n")
	return b.String()
}

// renderPrompt builds a single-pass prompt (used when no grader docs FS is available).
func renderPrompt(data promptData) string {
	return renderImplementationPrompt(data, "")
}
