package suggest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	waza "github.com/microsoft/waza"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

type generateTestEngine struct {
	responses []*execution.ExecutionResponse
	callIdx   int
}

func (e *generateTestEngine) Initialize(context.Context) error { return nil }

func (e *generateTestEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if len(e.responses) == 0 {
		return nil, errors.New("no engine responses configured")
	}
	idx := e.callIdx
	e.callIdx++
	if idx < len(e.responses) {
		return e.responses[idx], nil
	}
	return e.responses[len(e.responses)-1], nil
}

func (e *generateTestEngine) Shutdown(context.Context) error { return nil }

func (e *generateTestEngine) SessionUsage(string) *models.UsageStats { return nil }

func writeGenerateSkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw := `---
name: suggest-skill
description: "Useful skill. USE FOR: summarize. DO NOT USE FOR: deploy."
---

# Suggest Skill

Summarize things.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o644))
	return dir
}

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

	data := buildPromptData(&sk, raw, DefaultCaseCount, "")
	prompt := renderSelectionPrompt(data)
	require.Contains(t, prompt, "selecting grader types")
	require.Contains(t, prompt, "Name: test-skill")
	require.Contains(t, prompt, "code: Assertion-based")
	require.Contains(t, prompt, "text: Text matching")
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

	data := buildPromptData(&sk, raw, DefaultCaseCount, "")
	graderDocs := "### `code` - Assertion-Based Grader\nSome docs here."
	prompt := renderImplementationPrompt(data, graderDocs)
	require.Contains(t, prompt, "Grader documentation for the types you should use")
	require.Contains(t, prompt, "Assertion-Based Grader")
	require.Contains(t, prompt, "NEVER use bare strings")
}

func TestParseGraderSelectionStructured(t *testing.T) {
	input := "graders:\n  - code\n  - text\n  - skill_invocation\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "text", "skill_invocation"}, result)
}

func TestParseGraderSelectionBareList(t *testing.T) {
	input := "- code\n- text\n- file\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "text", "file"}, result)
}

func TestParseGraderSelectionFiltersInvalid(t *testing.T) {
	input := "graders:\n  - code\n  - not_a_real_grader\n  - text\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"code", "text"}, result)
}

func TestParseGraderSelectionCodeFence(t *testing.T) {
	input := "```yaml\ngraders:\n  - text\n  - diff\n```\n"
	result := parseGraderSelection(input)
	require.Equal(t, []string{"text", "diff"}, result)
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
	result := LoadGraderDocs(nil, []string{"code", "text"})
	require.Equal(t, "", result)
}

func TestLoadGraderDocsFromEmbeddedFS(t *testing.T) {
	docs := LoadGraderDocs(waza.GraderDocsFS, []string{"code", "text"})
	require.Contains(t, docs, "Assertion-Based Grader")
	require.Contains(t, docs, "Text Matching Grader")
	// unknown types silently skipped
	docs2 := LoadGraderDocs(waza.GraderDocsFS, []string{"not_a_type"})
	require.Equal(t, "", docs2)
}

func TestGenerateSurfacesGraderSelectionEngineError(t *testing.T) {
	engine := &generateTestEngine{responses: []*execution.ExecutionResponse{
		{Success: false, ErrorMsg: "upstream rejected grader selection"},
	}}

	_, err := Generate(context.Background(), engine, Options{
		SkillPath:  writeGenerateSkill(t),
		GraderDocs: waza.GraderDocsFS,
	})

	require.ErrorContains(t, err, "grader selection: engine execution failed: upstream rejected grader selection")
	require.NotContains(t, err.Error(), "parsing suggest response")
}

func TestGenerateSurfacesImplementationEngineError(t *testing.T) {
	engine := &generateTestEngine{responses: []*execution.ExecutionResponse{
		{Success: true, FinalOutput: "graders:\n  - text\n"},
		{Success: false, ErrorMsg: "upstream rejected implementation request"},
	}}

	_, err := Generate(context.Background(), engine, Options{
		SkillPath:  writeGenerateSkill(t),
		GraderDocs: waza.GraderDocsFS,
	})

	require.ErrorContains(t, err, "getting suggestions: engine execution failed: upstream rejected implementation request")
	require.NotContains(t, err.Error(), "parsing suggest response")
}

func TestParseResponseStructuredYAML(t *testing.T) {
	resp := "```yaml\neval_yaml: |\n  name: generated-eval\n  description: generated\n  skill: sample\n  version: \"1.0\"\n  config:\n    trials_per_task: 1\n    timeout_seconds: 120\n    parallel: false\n    executor: mock\n    model: test\n  graders:\n    - type: code\n      name: has_output\n      config:\n        assertions:\n          - \\\"len(output) > 0\\\"\n  metrics:\n    - name: completion\n      weight: 1.0\n      threshold: 0.8\n  tasks:\n    - \"tasks/*.yaml\"\ntasks:\n  - path: tasks/basic.yaml\n    confidence: 0.9\n    rationale: \"SKILL.md overview\"\n    content: |\n      id: basic-001\n      name: Basic\n      inputs:\n        prompt: \"hello\"\nfixtures:\n  - path: fixtures/sample.txt\n    content: |\n      sample\n```"

	s, err := ParseResponse(resp)
	require.NoError(t, err)
	require.Equal(t, 1, len(s.Tasks))
	require.Equal(t, "tasks/basic.yaml", s.Tasks[0].Path)
	require.Equal(t, 1, len(s.Fixtures))
}

func TestParseResponseIncludesCaseMetadata(t *testing.T) {
	resp := `eval_yaml: |
  name: generated-eval
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
    - type: text
      name: has_keywords
      config:
        contains:
          - hello
  metrics:
    - name: completion
      weight: 1.0
      threshold: 0.8
  tasks:
    - "tasks/*.yaml"
tasks:
  - path: tasks/basic.yaml
    confidence: 0.82
    rationale: "SKILL.md description USE FOR: summarize"
    content: |
      id: basic-001
      name: Basic
      inputs:
        prompt: "hello"
`

	s, err := ParseResponse(resp)
	require.NoError(t, err)
	require.Len(t, s.Tasks, 1)
	require.NotNil(t, s.Tasks[0].Confidence)
	require.Equal(t, 0.82, *s.Tasks[0].Confidence)
	require.Equal(t, "SKILL.md description USE FOR: summarize", s.Tasks[0].Rationale)
}

func TestParseResponseInvalid(t *testing.T) {
	_, err := ParseResponse("not valid yaml")
	require.Error(t, err)
}

func TestParseResponseRequiresCaseMetadata(t *testing.T) {
	resp := `eval_yaml: |
  name: generated-eval
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
    - type: text
      name: has_keywords
      config:
        contains:
          - hello
  metrics:
    - name: completion
      weight: 1.0
      threshold: 0.8
  tasks:
    - "tasks/*.yaml"
tasks:
  - path: tasks/basic.yaml
    content: |
      id: basic-001
      name: Basic
      inputs:
        prompt: "hello"
`

	_, err := ParseResponse(resp)
	require.ErrorContains(t, err, "confidence is required")
}

func TestParseResponseEmpty(t *testing.T) {
	_, err := ParseResponse("")
	require.ErrorContains(t, err, "empty suggest response")
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
// then unmarshaled back into a EvalSpec without loss.
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
  - type: text
    name: check_keywords
    config:
      contains:
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
	require.Contains(t, err.Error(), "missing property 'name'")
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
	require.Contains(t, err.Error(), "missing property 'type'")
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
			{Path: "tasks/basic.yaml", Confidence: testConfidence(0.9), Rationale: "SKILL.md overview", Content: "id: basic-001\nname: Basic\ninputs:\n  prompt: \"hello\""},
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

func TestWriteToDirMergesExistingEvalYAML(t *testing.T) {
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	existingEval := `name: curated-eval
description: curated
skill: sample
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  parallel: false
  executor: mock
  model: test
graders:
  - type: text
    name: has_text
    config:
      contains:
        - hello
metrics:
  - name: completion
    weight: 1.0
    threshold: 0.8
tasks:
  - "custom/*.yaml"
`
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "eval.yaml"), []byte(existingEval), 0o644))

	written, err := s.WriteToDirWithOptions(outDir, WriteOptions{})
	require.NoError(t, err)
	require.Contains(t, written, filepath.Join(outDir, "eval.yaml"))

	evalData, err := os.ReadFile(filepath.Join(outDir, "eval.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(evalData), "name: curated-eval")
	require.Contains(t, string(evalData), "custom/*.yaml")
	require.Contains(t, string(evalData), "tasks/*.yaml")
	require.NotContains(t, string(evalData), "generated-eval")
}

func TestFocusCategoriesAreAcceptedInPrompt(t *testing.T) {
	raw := `---
name: focus-skill
description: "Useful skill. USE FOR: summarize. DO NOT USE FOR: deploy."
---
# Focus Skill
`
	var sk skill.Skill
	require.NoError(t, sk.UnmarshalText([]byte(raw)))
	sk.Path = filepath.Join(t.TempDir(), "SKILL.md")

	for _, category := range AllFocusCategories() {
		parsed, err := ParseFocusCategory(string(category))
		require.NoError(t, err)

		data := buildPromptData(&sk, raw, 2, parsed)
		prompt := renderImplementationPrompt(data, "")

		require.Contains(t, prompt, fmt.Sprintf("Generate exactly %d task case(s).", 2))
		require.Contains(t, prompt, string(category))
	}
}

func TestParseFocusCategoryRejectsInvalid(t *testing.T) {
	_, err := ParseFocusCategory("not-a-focus")
	require.ErrorContains(t, err, "invalid focus")
}

func TestWriteToDirRefusesExistingTaskIDWithoutForce(t *testing.T) {
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outDir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "tasks", "existing.yaml"), []byte(`id: basic-001
name: Existing
inputs:
  prompt: "existing"
`), 0o644))

	_, err := s.WriteToDirWithOptions(outDir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), `existing task id "basic-001"`)
	require.Contains(t, err.Error(), "--force")
}

func TestWriteToDirForceOverwritesExistingTaskID(t *testing.T) {
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outDir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outDir, "tasks", "basic.yaml"), []byte(`id: basic-001
name: Existing
inputs:
  prompt: "existing"
`), 0o644))

	written, err := s.WriteToDirWithOptions(outDir, WriteOptions{Force: true})
	require.NoError(t, err)
	require.Len(t, written, 3)

	taskData, err := os.ReadFile(filepath.Join(outDir, "tasks", "basic.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(taskData), `prompt: "hello"`)
}

func TestWriteToDirForceRemovesExistingTaskIDAtDifferentPath(t *testing.T) {
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outDir, "tasks"), 0o755))
	existingPath := filepath.Join(outDir, "tasks", "existing.yaml")
	require.NoError(t, os.WriteFile(existingPath, []byte(`id: basic-001
name: Existing
inputs:
  prompt: "existing"
`), 0o644))

	_, err := s.WriteToDirWithOptions(outDir, WriteOptions{Force: true})
	require.NoError(t, err)
	require.NoFileExists(t, existingPath)
	require.FileExists(t, filepath.Join(outDir, "tasks", "basic.yaml"))
}

func TestWriteToDirValidatesGeneratedTaskSchemaBeforeWrite(t *testing.T) {
	s := validSuggestionForWrite()
	s.Tasks[0].Content = `id: basic-001
name: Basic
prompt: "unknown top-level field"
`

	outDir := t.TempDir()
	_, err := s.WriteToDirWithOptions(outDir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "task schema validation failed")
	require.Contains(t, err.Error(), "prompt")
	require.NoFileExists(t, filepath.Join(outDir, "eval.yaml"))
}

func TestWriteToDirRejectsSymlinkEvalYAML(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows environments")
	}
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outDir, "tasks"), 0o755))
	existingTask := filepath.Join(outDir, "tasks", "existing.yaml")
	require.NoError(t, os.WriteFile(existingTask, []byte(`id: basic-001
name: Existing
inputs:
  prompt: "existing"
`), 0o644))
	target := filepath.Join(t.TempDir(), "outside-eval.yaml")
	require.NoError(t, os.WriteFile(target, []byte(validEvalYAML()), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(outDir, "eval.yaml")))

	_, err := s.WriteToDirWithOptions(outDir, WriteOptions{Force: true})
	require.ErrorContains(t, err, "eval.yaml is a symlink")
	require.FileExists(t, existingTask)
}

func TestWriteToDirRejectsSymlinkFixtureTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows environments")
	}
	s := validSuggestionForWrite()
	outDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outDir, "fixtures"), 0o755))
	target := filepath.Join(t.TempDir(), "outside-fixture.txt")
	require.NoError(t, os.WriteFile(target, []byte("outside"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(outDir, "fixtures", "sample.txt")))

	_, err := s.WriteToDirWithOptions(outDir, WriteOptions{Force: true})
	require.ErrorContains(t, err, "symlink")
}

func TestWriteToDirRejectsTaskPathOutsideTasks(t *testing.T) {
	s := validSuggestionForWrite()
	s.Tasks[0].Path = "eval.yaml"

	_, err := s.WriteToDirWithOptions(t.TempDir(), WriteOptions{Force: true})
	require.ErrorContains(t, err, "must be under tasks/")
}

func TestWriteToDirRejectsUnsafeGeneratedEvalTaskRef(t *testing.T) {
	s := validSuggestionForWrite()
	s.EvalYAML = strings.Replace(s.EvalYAML, `tasks/*.yaml`, `../outside/*.yaml`, 1)

	_, err := s.WriteToDirWithOptions(t.TempDir(), WriteOptions{Force: true})
	require.ErrorContains(t, err, "must not contain '..' segments")
}

func TestWriteToDirRejectsDuplicateGeneratedPath(t *testing.T) {
	s := validSuggestionForWrite()
	s.Tasks = append(s.Tasks, GeneratedFile{
		Path:       "tasks/basic.yaml",
		Confidence: testConfidence(0.8),
		Rationale:  "SKILL.md second span",
		Content:    "id: basic-002\nname: Basic Two\ninputs:\n  prompt: \"hello again\"",
	})

	_, err := s.WriteToDirWithOptions(t.TempDir(), WriteOptions{Force: true})
	require.ErrorContains(t, err, "duplicate generated path: tasks/basic.yaml")
}

func TestWriteToDirRejectsDuplicateGeneratedTaskIDWithForce(t *testing.T) {
	s := validSuggestionForWrite()
	s.Tasks = append(s.Tasks, GeneratedFile{
		Path:       "tasks/other.yaml",
		Confidence: testConfidence(0.8),
		Rationale:  "SKILL.md second span",
		Content:    "id: basic-001\nname: Other\ninputs:\n  prompt: \"hello again\"",
	})

	_, err := s.WriteToDirWithOptions(t.TempDir(), WriteOptions{Force: true})
	require.ErrorContains(t, err, `duplicate generated task id "basic-001"`)
}

func TestValidateInvalidConfigFields(t *testing.T) {
	invalidEvalYAML := `name: bad-eval
description: has unknown field
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 120
  executor: mock
  model: test
  unknown_field: should cause error
`
	err := validateEvalYAML(invalidEvalYAML)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown_field")
}

func TestValidateInvalidEvalSpecFields(t *testing.T) {
	invalidEvalYAML := `name: bad-eval
description: has unknown field
skill: test-skill
version: "1.0"
unknown_field: should cause error
config:
  trials_per_task: 1
  timeout_seconds: 120
  executor: mock
  model: test
`
	err := validateEvalYAML(invalidEvalYAML)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown_field")
}

func validSuggestionForWrite() *Suggestion {
	return &Suggestion{
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
			{
				Path:       "tasks/basic.yaml",
				Confidence: testConfidence(0.9),
				Rationale:  "SKILL.md overview",
				Content:    "id: basic-001\nname: Basic\ninputs:\n  prompt: \"hello\"",
			},
		},
		Fixtures: []GeneratedFile{
			{Path: "fixtures/sample.txt", Content: "sample"},
		},
	}
}

func testConfidence(value float64) *float64 {
	return &value
}
