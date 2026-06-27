package suggest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderSelectionPrompt_Structure(t *testing.T) {
	data := promptData{
		SkillName:      "my-skill",
		Description:    "A skill that does things",
		Triggers:       "do things, make stuff",
		AntiTriggers:   "break things",
		ContentSummary: "## Overview | Does things",
	}

	prompt := renderSelectionPrompt(data)
	assert.Contains(t, prompt, "selecting grader types")
	assert.Contains(t, prompt, "Name: my-skill")
	assert.Contains(t, prompt, "Description: A skill that does things")
	assert.Contains(t, prompt, "Triggers (USE FOR): do things, make stuff")
	assert.Contains(t, prompt, "Anti-triggers (DO NOT USE FOR): break things")
	assert.Contains(t, prompt, "Content summary: ## Overview | Does things")
	assert.Contains(t, prompt, "YAML list of grader type names")
}

func TestRenderSelectionPrompt_IncludesGraderSummaries(t *testing.T) {
	data := promptData{SkillName: "test"}
	prompt := renderSelectionPrompt(data)
	// Should include the grader summaries inline
	for _, gtype := range AvailableGraderTypes() {
		assert.Contains(t, prompt, gtype)
	}
}

func TestRenderImplementationPrompt_WithGraderDocs(t *testing.T) {
	data := promptData{
		SkillName:    "test-skill",
		Description:  "A test skill",
		SkillContent: "# Test Skill\nDoes testing.",
	}
	graderDocs := "### `code` - Assertion-Based\nRun assertions against output."

	prompt := renderImplementationPrompt(data, graderDocs)
	assert.Contains(t, prompt, "generating a waza evaluation suite")
	assert.Contains(t, prompt, "Name: test-skill")
	assert.Contains(t, prompt, "Grader documentation for the types you should use")
	assert.Contains(t, prompt, "Assertion-Based")
	assert.Contains(t, prompt, evalYAMLSchemaSummary)
	assert.Contains(t, prompt, exampleEvalYAML)
	assert.Contains(t, prompt, "Skill content (SKILL.md)")
	assert.Contains(t, prompt, "Does testing.")
}

func TestRenderImplementationPrompt_WithoutGraderDocs(t *testing.T) {
	data := promptData{
		SkillName:    "test-skill",
		Description:  "A test skill",
		SkillContent: "# Test",
	}

	prompt := renderImplementationPrompt(data, "")
	assert.Contains(t, prompt, "Name: test-skill")
	assert.NotContains(t, prompt, "Grader documentation for the types you should use")
}

func TestRenderImplementationPrompt_ContainsRequirements(t *testing.T) {
	data := promptData{SkillName: "x"}
	prompt := renderImplementationPrompt(data, "")
	// Key requirements that must be in the prompt
	assert.Contains(t, prompt, "NEVER use bare strings")
	assert.Contains(t, prompt, "required_skills")
	assert.Contains(t, prompt, "Task YAML must use inputs")
	assert.Contains(t, prompt, "Generate exactly 3 task case(s)")
	assert.Contains(t, prompt, "confidence")
	assert.Contains(t, prompt, "rationale")
}

func TestRenderPrompt_IsSinglePassFallback(t *testing.T) {
	data := promptData{
		SkillName:    "single-pass-skill",
		Description:  "Does stuff",
		SkillContent: "# Content",
	}
	prompt := renderPrompt(data)
	// renderPrompt should call renderImplementationPrompt with empty graderDocs
	assert.Contains(t, prompt, "Name: single-pass-skill")
	assert.NotContains(t, prompt, "Grader documentation for the types you should use")
	assert.Contains(t, prompt, "generating a waza evaluation suite")
}

func TestRenderSelectionPrompt_EmptyFields(t *testing.T) {
	data := promptData{
		SkillName:      "",
		Description:    "",
		Triggers:       "",
		AntiTriggers:   "",
		ContentSummary: "",
	}
	prompt := renderSelectionPrompt(data)
	require.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "Name: ")
	assert.Contains(t, prompt, "selecting grader types")
}

func TestRenderImplementationPrompt_EmptySkillContent(t *testing.T) {
	data := promptData{
		SkillName:    "empty-skill",
		SkillContent: "",
	}
	prompt := renderImplementationPrompt(data, "")
	assert.Contains(t, prompt, "Skill content (SKILL.md)")
	assert.Contains(t, prompt, "Name: empty-skill")
}

func TestPromptDataStruct_AllFieldsUsed(t *testing.T) {
	data := promptData{
		SkillName:      "skill-name",
		Description:    "desc-value",
		Triggers:       "trigger-value",
		AntiTriggers:   "anti-trigger-value",
		ContentSummary: "summary-value",
		GraderTypes:    "- code\n- text",
		SkillContent:   "content-value",
	}

	// Selection prompt uses SkillName, Description, Triggers, AntiTriggers, ContentSummary
	sel := renderSelectionPrompt(data)
	assert.Contains(t, sel, "skill-name")
	assert.Contains(t, sel, "desc-value")
	assert.Contains(t, sel, "trigger-value")
	assert.Contains(t, sel, "anti-trigger-value")
	assert.Contains(t, sel, "summary-value")

	// Implementation prompt uses all fields
	impl := renderImplementationPrompt(data, "docs-here")
	assert.Contains(t, impl, "skill-name")
	assert.Contains(t, impl, "content-value")
	assert.Contains(t, impl, "docs-here")
}
