package suggest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	waza "github.com/spboyer/waza"
	"github.com/spboyer/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

func TestBuildPromptIncludesSkillMetadata(t *testing.T) {
	raw := `---
name: prompt-skill
description: "Useful skill. USE FOR: summarize, explain. DO NOT USE FOR: coding, deployment."
---

# Prompt Skill

## Overview
This skill summarizes docs.
`

	var sk skill.Skill
	require.NoError(t, sk.UnmarshalText([]byte(raw)))
	sk.Path = filepath.Join(t.TempDir(), "SKILL.md")

	prompt := BuildPrompt(&sk, raw)
	require.Contains(t, prompt, "Name: prompt-skill")
	require.Contains(t, prompt, "Triggers (USE FOR): summarize, explain")
	require.Contains(t, prompt, "Anti-triggers (DO NOT USE FOR): coding, deployment")
	require.Contains(t, prompt, "waza eval YAML schema summary")
	require.Contains(t, prompt, "Skill content (SKILL.md)")
	require.Contains(t, prompt, "NEVER use bare strings")
	require.Contains(t, prompt, "required_skills")
	require.Contains(t, prompt, "Task YAML must use inputs")
}

func TestSelectionPromptIncludesGraderSummaries(t *testing.T) {
	raw := `---
name: test-skill
description: "A test skill."
---
# Test
`
	var sk skill.Skill
	require.NoError(t, sk.UnmarshalText([]byte(raw)))
	sk.Path = filepath.Join(t.TempDir(), "SKILL.md")

	data := buildPromptData(&sk, raw)
	prompt := renderSelectionPrompt(data)
	require.Contains(t, prompt, "selecting grader types")
	require.Contains(t, prompt, "Name: test-skill")
	require.Contains(t, prompt, "code: Assertion-based")
	require.Contains(t, prompt, "keyword: Keyword check")
	require.Contains(t, prompt, "skill_invocation: Skill invocation")
}

func TestImplementationPromptIncludesGraderDocs(t *testing.T) {
	raw := `---
name: test-skill
description: "A test skill."
---
# Test
`
	var sk skill.Skill
	require.NoError(t, sk.UnmarshalText([]byte(raw)))
	sk.Path = filepath.Join(t.TempDir(), "SKILL.md")

	data := buildPromptData(&sk, raw)
	graderDocs := "### `code` - Assertion-Based Grader\nSome docs here."
	prompt := renderImplementationPrompt(data, graderDocs)
	require.Contains(t, prompt, "Grader documentation for the types you should use")
	require.Contains(t, prompt, "Assertion-Based Grader")
	require.Contains(t, prompt, "NEVER use bare strings")
}

func TestParseGraderSelectionStructured(t *testing.T) {
	input := "graders:\n  - code\n  - keyword\n  - skill_invocation\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "keyword", "skill_invocation"}, result)
}

func TestParseGraderSelectionBareList(t *testing.T) {
	input := "- code\n- regex\n- file\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "regex", "file"}, result)
}

func TestParseGraderSelectionFiltersInvalid(t *testing.T) {
	input := "graders:\n  - code\n  - not_a_real_grader\n  - keyword\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "keyword"}, result)
}

func TestParseGraderSelectionCodeFence(t *testing.T) {
	input := "```yaml\ngraders:\n  - regex\n  - diff\n```\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"regex", "diff"}, result)
}

func TestParseGraderSelectionEmpty(t *testing.T) {
	result := parseGraderSelection("")
	require.Nil(t, result)
}

func TestGraderSummariesListsAllTypes(t *testing.T) {
	summaries := GraderSummaries()
	for _, graderType := range AvailableGraderTypes() {
		require.Contains(t, summaries, graderType+":")
	}
}

func TestLoadGraderDocsNilFS(t *testing.T) {
	result := LoadGraderDocs(nil, []string{"code", "regex"})
	require.Equal(t, "", result)
}

func TestLoadGraderDocsFromEmbeddedFS(t *testing.T) {
	docs := LoadGraderDocs(waza.GraderDocsFS, []string{"code", "keyword"})
	require.Contains(t, docs, "Assertion-Based Grader")
	require.Contains(t, docs, "Keyword Matching Grader")
	// unknown types silently skipped
	docs2 := LoadGraderDocs(waza.GraderDocsFS, []string{"not_a_type"})
	require.Equal(t, "", docs2)
}

func TestParseResponseStructuredYAML(t *testing.T) {
	resp := "```yaml\neval_yaml: |\n  name: generated-eval\n  description: generated\n  skill: sample\n  version: \"1.0\"\n  config:\n    trials_per_task: 1\n    timeout_seconds: 120\n    parallel: false\n    executor: mock\n    model: test\n  graders:\n    - type: code\n      name: has_output\n      config:\n        assertions:\n          - \\\"len(output) > 0\\\"\n  metrics:\n    - name: completion\n      weight: 1.0\n      threshold: 0.8\n  tasks:\n    - \"tasks/*.yaml\"\ntasks:\n  - path: tasks/basic.yaml\n    content: |\n      id: basic-001\n      name: Basic\n      inputs:\n        prompt: \"hello\"\nfixtures:\n  - path: fixtures/sample.txt\n    content: |\n      sample\n```"

	s, err := ParseResponse(resp)
	require.NoError(t, err)
	require.Equal(t, 1, len(s.Tasks))
	require.Equal(t, "tasks/basic.yaml", s.Tasks[0].Path)
	require.Equal(t, 1, len(s.Fixtures))
}

func TestParseResponseInvalid(t *testing.T) {
	_, err := ParseResponse("not valid yaml")
	require.Error(t, err)
}

// TestParseResponseRejectsBareStringGraders verifies that grader entries
// must be objects with at least a "name" field, not bare strings.
func TestParseResponseRejectsBareStringGraders(t *testing.T) {
	// This is what an LLM might produce: graders as plain strings.
	// It must be rejected because []GraderConfig can't unmarshal strings.
	resp := `eval_yaml: |
  name: bad-eval
  description: graders are bare strings
  skill: test-skill
  version: "1.0"
  config:
    trials_per_task: 1
    timeout_seconds: 120
    parallel: false
    executor: mock
    model: test
  graders:
    - some_custom_grader
    - another_grader
  metrics:
    - name: completion
      weight: 1.0
      threshold: 0.8
  tasks:
    - "tasks/*.yaml"
`
	_, err := ParseResponse(resp)
	require.Error(t, err, "bare-string grader entries should be rejected")
}

// TestEvalYAMLRoundTrip verifies that valid eval YAML can be marshaled and
// then unmarshalled back into a BenchmarkSpec without loss.
func TestEvalYAMLRoundTrip(t *testing.T) {
	evalYAML := `name: roundtrip-eval
description: test round-trip
skill: sample-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: copilot-sdk
  model: gpt-4o
graders:
  - type: keyword
    name: check_keywords
    config:
      must_include:
        - hello
  - type: skill_invocation
    name: skill_was_invoked
    config:
      required_skills:
        - my-skill
      mode: any_order
metrics:
  - name: task_completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	err := validateEvalYAML(evalYAML)
	require.NoError(t, err)
}

func TestValidateEvalYAMLRejectsGraderMissingName(t *testing.T) {
	evalYAML := `name: bad-eval
description: grader missing name
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  executor: mock
  model: test
graders:
  - type: code
    config:
      assertions:
        - "len(output) > 0"
metrics:
  - name: completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	err := validateEvalYAML(evalYAML)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required 'name'")
}

func TestValidateEvalYAMLRejectsGraderMissingType(t *testing.T) {
	evalYAML := `name: bad-eval
description: grader missing type
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  executor: mock
  model: test
graders:
  - name: orphan_grader
metrics:
  - name: completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	err := validateEvalYAML(evalYAML)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required 'type'")
}

func TestWriteToDirWritesFiles(t *testing.T) {
	s := &Suggestion{
		EvalYAML: `name: generated-eval
description: generated
skill: sample
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  parallel: false
  executor: mock
  model: test
graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 0"
metrics:
  - name: completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"`,
		Tasks: []GeneratedFile{
			{Path: "tasks/basic.yaml", Content: "id: basic-001\nname: Basic\ninputs:\n  prompt: \"hello\""},
		},
		Fixtures: []GeneratedFile{
			{Path: "fixtures/sample.txt", Content: "sample"},
		},
	}

	outDir := t.TempDir()
	written, err := s.WriteToDir(outDir)
	require.NoError(t, err)
	require.Len(t, written, 3)

	evalData, err := os.ReadFile(filepath.Join(outDir, "eval.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(evalData), "name: generated-eval")

	taskData, err := os.ReadFile(filepath.Join(outDir, "tasks", "basic.yaml"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(taskData), "id: basic-001"))
}
