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
  - type (code|prompt|text|file|json_schema|program|behavior|action_sequence|skill_invocation|diff|tool_constraint)
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
  - type: text
    name: no_errors
    config:
      regex_not_match:
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
	// Count is the desired number of test cases (<= 0 means use default).
	Count int
	// Focus is an optional focus category that steers the kinds of cases
	// the LLM should emit (e.g. "negative-triggers"). Empty means balanced.
	Focus string
}

// focusDirective returns prompt text that steers generation toward a focus
// category. Returns "" when focus is empty.
func focusDirective(focus string) string {
	switch FocusCategory(focus) {
	case "":
		return ""
	case FocusTriggers:
		return "FOCUS: Generate tasks that exercise the skill's positive trigger phrases " +
			"(the 'USE FOR' list). Each task should match a distinct trigger so we can verify " +
			"the skill activates when it should."
	case FocusNegativeTriggers:
		return "FOCUS: Generate tasks that look superficially like triggers but should NOT " +
			"invoke this skill. Use skill_invocation graders with forbidden_skills to assert " +
			"the skill stays out of these cases."
	case FocusEdgeFixtures:
		return "FOCUS: Generate tasks that stress edge cases in the input fixtures — empty " +
			"files, very large files, malformed inputs, unicode, ambiguous content. Each task " +
			"should carry a realistic edge-case fixture under fixtures/."
	case FocusDoNotUseFor:
		return "FOCUS: Generate tasks drawn from the skill's 'DO NOT USE FOR' anti-trigger " +
			"list. Each task must assert the skill refuses or routes elsewhere (use " +
			"skill_invocation forbidden_skills, or text graders that check for refusal language)."
	case FocusParameters:
		return "FOCUS: Generate tasks that vary the parameters and tool arguments the skill " +
			"would supply — required vs optional, valid vs invalid values, boundary numerics. " +
			"Use tool_constraint or action_sequence graders where appropriate."
	}
	return ""
}

// renderSelectionPrompt builds the pass-1 prompt that asks the LLM
// to choose appropriate grader types for the skill.
func renderSelectionPrompt(data promptData) string {
	var b strings.Builder
	b.WriteString("You are selecting grader types for a waza evaluation suite.\n")
	b.WriteString("Given the skill description below, choose which grader types are most appropriate.\n\n")
	b.WriteString("Return ONLY a YAML list of grader type names, one per line, like:\n")
	b.WriteString("```yaml\ngraders:\n  - code\n  - text\n  - skill_invocation\n```\n\n")
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
	b.WriteString("    confidence: 0.0-1.0\n")
	b.WriteString("    rationale: <one sentence pointing to the SKILL.md span this case came from>\n")
	b.WriteString("    content: |\n")
	b.WriteString("      <task yaml>\n")
	b.WriteString("fixtures:\n")
	b.WriteString("  - path: fixtures/<fixture-file>\n")
	b.WriteString("    content: |\n")
	b.WriteString("      <fixture content>\n\n")
	b.WriteString("Requirements:\n")
	b.WriteString("- Ensure eval_yaml is valid waza EvalSpec YAML.\n")
	b.WriteString("- Each grader entry MUST be an object with at least 'type' and 'name' fields. NEVER use bare strings like '- grader_name'. Always use '- name: grader_name' with a 'type' field.\n")
	b.WriteString("- Include required config fields for each grader type (see grader documentation below).\n")
	b.WriteString("- For skill_invocation graders, use required_skills + mode (exact_match, in_order, any_order) for positive checks, forbidden_skills for negative checks, or both together.\n")
	if data.Count > 0 {
		fmt.Fprintf(&b, "- Generate EXACTLY %d tasks. Do not generate more or fewer.\n", data.Count)
	} else {
		b.WriteString("- Include at least 3 diverse tasks and at least 1 negative/anti-trigger task.\n")
	}
	b.WriteString("- Use grader types from the allowed list only.\n")
	b.WriteString("- Keep task IDs deterministic and kebab-case.\n")
	b.WriteString("- Task YAML must use inputs: { prompt: ... } (do not use a top-level prompt field).\n")
	b.WriteString("- Task YAML must NOT include 'confidence' or 'rationale' inside the task content; those belong on the outer suggestion entry and will be stripped before writing.\n")
	b.WriteString("- Each task entry MUST carry a 'confidence' float in [0,1] and a 'rationale' string citing the SKILL.md span (e.g. \"matches USE FOR: summarize bullet 2\").\n")
	b.WriteString("- Make fixtures minimal and realistic for the tasks.\n")
	if directive := focusDirective(data.Focus); directive != "" {
		b.WriteString("- " + directive + "\n")
	}
	b.WriteString("\n")
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
