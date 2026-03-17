package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveSuggestionSkillPaths_DedupesAndSorts(t *testing.T) {
	parent := t.TempDir()
	a := filepath.Join(parent, "a")
	b := filepath.Join(parent, "b")
	require.NoError(t, os.MkdirAll(a, 0o755))
	require.NoError(t, os.MkdirAll(b, 0o755))

	spec := &models.EvalSpec{
		Config: models.EvalConfig{
			SkillPaths: []string{b, a, b},
		},
	}

	got := resolveSuggestionSkillPaths(spec, filepath.Join(parent, "eval.yaml"))
	require.Equal(t, []string{parent, a, b}, got)
}

func TestResolveSuggestionSkillPaths_IncludesEvaluatedSkillDirectory(t *testing.T) {
	root := t.TempDir()
	specDir := filepath.Join(root, "evals")
	skillRoot := filepath.Join(root, "skills")
	evaluatedSkillDir := filepath.Join(skillRoot, "my-skill")
	require.NoError(t, os.MkdirAll(specDir, 0o755))
	require.NoError(t, os.MkdirAll(evaluatedSkillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evaluatedSkillDir, "SKILL.md"), []byte("# My Skill"), 0o644))

	spec := &models.EvalSpec{
		SkillName: "my-skill",
		Config: models.EvalConfig{
			SkillPaths: []string{skillRoot},
		},
	}

	got := resolveSuggestionSkillPaths(spec, filepath.Join(specDir, "eval.yaml"))
	assert.Contains(t, got, evaluatedSkillDir)
}

func TestMaybeGenerateSuggestionReport_SkipsWhenNoFailures(t *testing.T) {
	oldSuggest := suggestFlag
	suggestFlag = true
	t.Cleanup(func() { suggestFlag = oldSuggest })

	spec := &models.EvalSpec{
		SkillName: "test-skill",
		Config: models.EvalConfig{
			EngineType: "mock",
			ModelID:    "test-model",
		},
	}
	outcome := &models.EvaluationOutcome{
		Digest: models.OutcomeDigest{
			TotalTests: 1,
			Succeeded:  1,
			Failed:     0,
			Errors:     0,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "pass-1",
				DisplayName: "passing-test",
				Status:      models.StatusPassed,
			},
		},
	}

	engine := execution.NewMockEngine("test-model")
	report, err := generateEvalAnalysis(context.Background(), engine, spec, filepath.Join(t.TempDir(), "eval.yaml"), outcome, nil)
	require.NoError(t, err)
	assert.Empty(t, report)
}

func TestBuildNoSuggestionsError_IncludesSessionTranscript(t *testing.T) {
	msg := "Need more details before answering."
	toolName := "rg"
	toolCallID := "call-1"
	succeeded := true
	toolResultText := "matched 1 file"

	err := buildNoSuggestionsError(&execution.ExecutionResponse{
		Events: []copilot.SessionEvent{
			{
				Type: copilot.AssistantMessage,
				Data: copilot.Data{Content: &msg},
			},
			{
				Type: copilot.ToolExecutionStart,
				Data: copilot.Data{
					ToolName:   &toolName,
					ToolCallID: &toolCallID,
					Arguments:  map[string]any{"pattern": "foo"},
				},
			},
			{
				Type: copilot.ToolExecutionComplete,
				Data: copilot.Data{
					ToolName:   &toolName,
					ToolCallID: &toolCallID,
					Success:    &succeeded,
					Result: &copilot.Result{
						Content: &toolResultText,
					},
				},
			},
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no suggestions from Copilot. Session transcript:")
	assert.Contains(t, err.Error(), "agent: Need more details before answering.")
	assert.Contains(t, err.Error(), "tool start: rg")
	assert.Contains(t, err.Error(), "tool result: tool=rg success=true")
}

func TestBuildNoSuggestionsError_FallsBackToEventTypes(t *testing.T) {
	err := buildNoSuggestionsError(&execution.ExecutionResponse{
		Events: []copilot.SessionEvent{
			{Type: copilot.SessionIdle},
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "event[1]: "+string(copilot.SessionIdle))
}

func TestBuildNoSuggestionsError_WithNoEvents(t *testing.T) {
	err := buildNoSuggestionsError(&execution.ExecutionResponse{})
	require.ErrorContains(t, err, "no suggestions from Copilot (no session events captured)")
}

func TestBuildRunSuggestionPrompt_IncludesOnlyFailureEvidence(t *testing.T) {
	skillDir := t.TempDir()
	secretContent := "UNIQUE_SKILL_CONTENT_SHOULD_NOT_APPEAR_IN_PROMPT"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(secretContent), 0o644))

	spec := &models.EvalSpec{
		SkillName: "test-skill",
		Config: models.EvalConfig{
			EngineType: "copilot-sdk",
			ModelID:    "gpt-4o",
			SkillPaths: []string{skillDir},
		},
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "must-mention-foo",
				Parameters: models.TextGraderParameters{Contains: []string{"foo"}},
			},
		},
	}

	msg := "Need more details before answering."
	toolName := "rg"
	toolCallID := "call-1"
	toolResultText := "matched 1 file"
	succeeded := true

	failingTests := []models.TestOutcome{
		{
			TestID:      "fail-1",
			DisplayName: "failing-test",
			Status:      models.StatusFailed,
			Runs: []models.RunResult{
				{
					RunNumber:   1,
					Status:      models.StatusFailed,
					FinalOutput: "Short and wrong answer",
					Validations: map[string]models.GraderResults{
						"must-mention-foo": {
							Name:     "must-mention-foo",
							Passed:   false,
							Score:    0.0,
							Feedback: "output missing foo",
						},
					},
					Transcript: []models.TranscriptEvent{
						{
							SessionEvent: copilot.SessionEvent{
								Type: copilot.AssistantMessage,
								Data: copilot.Data{Content: &msg},
							},
						},
						{
							SessionEvent: copilot.SessionEvent{
								Type: copilot.ToolExecutionStart,
								Data: copilot.Data{
									ToolName:   &toolName,
									ToolCallID: &toolCallID,
									Arguments:  map[string]any{"pattern": "foo"},
								},
							},
						},
						{
							SessionEvent: copilot.SessionEvent{
								Type: copilot.ToolExecutionComplete,
								Data: copilot.Data{
									ToolName:   &toolName,
									ToolCallID: &toolCallID,
									Success:    &succeeded,
									Result: &copilot.Result{
										Content: &toolResultText,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	testDefinitions := map[string]string{
		"fail-1": "id: fail-1\nname: failing-test\ninputs:\n  prompt: explain x",
	}

	failedTriggers := []models.TriggerResult{
		{
			Prompt:        "Write me a new API endpoint",
			ShouldTrigger: false,
			DidTrigger:    true,
			FinalOutput:   "I invoked the skill unexpectedly",
		},
	}

	prompt := buildRunAnalysisPrompt(spec, failingTests, failedTriggers, testDefinitions)

	assert.Contains(t, prompt, "Global graders from eval.yaml")
	assert.Contains(t, prompt, "must-mention-foo")
	assert.Contains(t, prompt, "failing-test")
	assert.Contains(t, prompt, "id: fail-1")
	assert.Contains(t, prompt, "agent: Need more details before answering.")
	assert.Contains(t, prompt, "tool start: rg")
	assert.Contains(t, prompt, "tool result:")
	assert.Contains(t, prompt, "### Prompt: \"Write me a new API endpoint\"")
	assert.Contains(t, prompt, "Given this prompt the agent should not have used the skill, however it did.")
	assert.NotContains(t, prompt, skillDir)
	assert.NotContains(t, prompt, "Total tests:")
	assert.NotContains(t, prompt, "Success rate:")
	assert.NotContains(t, prompt, secretContent)
}

func TestBuildRunSuggestionPrompt_OmitsBenchmarkSectionWhenNoBenchmarkFailures(t *testing.T) {
	spec := &models.EvalSpec{
		SkillName: "test-skill",
		Config: models.EvalConfig{
			EngineType: "copilot-sdk",
			ModelID:    "gpt-4o",
		},
	}

	failedTriggers := []models.TriggerResult{
		{
			Prompt:        "Write me a new API endpoint",
			ShouldTrigger: false,
			DidTrigger:    true,
		},
	}

	prompt := buildRunAnalysisPrompt(spec, nil, failedTriggers, nil)

	assert.Contains(t, prompt, `### Prompt: "Write me a new API endpoint"`)
	assert.Contains(t, prompt, "Given this prompt the agent should not have used the skill, however it did.")
	assert.NotContains(t, prompt, "### Test:")
}

func TestBuildRunSuggestionPrompt_OmitsTriggerSectionWhenNoTriggerFailures(t *testing.T) {
	spec := &models.EvalSpec{
		SkillName: "test-skill",
		Config: models.EvalConfig{
			EngineType: "copilot-sdk",
			ModelID:    "gpt-4o",
		},
	}

	failingTests := []models.TestOutcome{
		{
			TestID:      "fail-1",
			DisplayName: "failing-test",
			Status:      models.StatusFailed,
		},
	}

	testDefinitions := map[string]string{
		"fail-1": "id: fail-1\nname: failing-test\ninputs:\n  prompt: explain x",
	}

	prompt := buildRunAnalysisPrompt(spec, failingTests, nil, testDefinitions)

	assert.Contains(t, prompt, "### Test: failing-test (`fail-1`)")
	assert.NotContains(t, prompt, "### Prompt:")
}

func TestBuildRunSuggestionPrompt_IncludesGraderDocs(t *testing.T) {
	spec := &models.EvalSpec{
		SkillName: "test-skill",
		Config: models.EvalConfig{
			EngineType: "copilot-sdk",
			ModelID:    "gpt-4o",
		},
		Graders: []models.GraderConfig{
			{Kind: models.GraderKindText, Identifier: "format-check"},
		},
	}

	failingTests := []models.TestOutcome{
		{
			TestID:      "fail-1",
			DisplayName: "failing-test",
			Status:      models.StatusFailed,
			Runs: []models.RunResult{
				{
					RunNumber: 1,
					Status:    models.StatusFailed,
					Validations: map[string]models.GraderResults{
						"kw-check": {
							Name:     "kw-check",
							Type:     models.GraderKindText,
							Passed:   false,
							Score:    0.0,
							Feedback: "missing keyword",
						},
					},
				},
			},
		},
	}

	prompt := buildRunAnalysisPrompt(spec, failingTests, nil, nil)

	// Should include the grader reference section
	assert.Contains(t, prompt, "## Grader reference")
	// Should include docs for the regex grader (from spec) and keyword grader (from failed validation)
	assert.Contains(t, prompt, "--- text grader ---")
	// Should include actual doc content
	assert.Contains(t, prompt, "Text Matching")
}

func TestBuildGraderDocsSection_EmptyWhenNoGraders(t *testing.T) {
	spec := &models.EvalSpec{}
	result := buildGraderDocsSection(spec, nil)
	assert.Empty(t, result)
}

func TestCollectFailedGraderKinds(t *testing.T) {
	spec := &models.EvalSpec{
		Graders: []models.GraderConfig{
			{Kind: models.GraderKindInlineScript, Identifier: "assertions"},
		},
	}
	failingTests := []models.TestOutcome{
		{
			Runs: []models.RunResult{
				{
					Validations: map[string]models.GraderResults{
						"pass": {Type: models.GraderKindDiff, Passed: true},
						"fail": {Type: models.GraderKindText, Passed: false},
					},
				},
			},
		},
	}

	kinds := collectFailedGraderKinds(spec, failingTests)

	// Global grader from spec (always included)
	assert.True(t, kinds["code"])
	// Failed grader from validation
	assert.True(t, kinds["text"])
	// Passed grader should not be included
	assert.False(t, kinds["diff"])
}

func TestWriteSuggestionTranscript_WritesFile(t *testing.T) {
	dir := t.TempDir()

	orig := transcriptDir
	transcriptDir = dir
	t.Cleanup(func() { transcriptDir = orig })

	msg := "Here are my suggestions."
	res := &execution.ExecutionResponse{
		FinalOutput: "Suggestion report text",
		Success:     true,
		DurationMs:  1500,
		Events: []copilot.SessionEvent{
			{
				Type: copilot.AssistantMessage,
				Data: copilot.Data{Content: &msg},
			},
		},
	}

	writeSuggestionTranscript("test prompt", res)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "suggestion-request-")

	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"task_id": "waza-suggest"`)
	assert.Contains(t, string(data), `"task_name": "suggestion-request"`)
	assert.Contains(t, string(data), "test prompt")
	assert.Contains(t, string(data), "Suggestion report text")
}

func TestWriteSuggestionTranscript_SkipsWhenNoDirConfigured(t *testing.T) {
	orig := transcriptDir
	transcriptDir = ""
	t.Cleanup(func() { transcriptDir = orig })

	// Should not panic or error
	writeSuggestionTranscript("test prompt", &execution.ExecutionResponse{
		FinalOutput: "report",
		Success:     true,
	})
}

func TestLoadSkillResources_LoadsTextFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# My Skill"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "graders"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "graders", "check.py"), []byte("print('ok')"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte("name: test"), 0o644))

	resources := loadSkillResources([]string{dir})

	paths := make(map[string]string)
	for _, r := range resources {
		paths[r.Path] = string(r.Content)
	}

	assert.Equal(t, "# My Skill", paths["SKILL.md"])
	assert.Equal(t, "print('ok')", paths["graders/check.py"])
	assert.Equal(t, "name: test", paths["eval.yaml"])
}

func TestLoadSkillResources_SkipsBinaryAndHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "image.png"), []byte("binary"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("git"), 0o644))

	resources := loadSkillResources([]string{dir})

	paths := make(map[string]bool)
	for _, r := range resources {
		paths[r.Path] = true
	}

	assert.True(t, paths["SKILL.md"], "SKILL.md should be loaded")
	assert.False(t, paths["image.png"], "binary files should be skipped")
	assert.False(t, paths[".git/config"], "hidden dirs should be skipped")
}

func TestLoadSkillResources_DeduplicatesAcrossPaths(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "SKILL.md"), []byte("first"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "SKILL.md"), []byte("second"), 0o644))

	resources := loadSkillResources([]string{dir1, dir2})

	count := 0
	for _, r := range resources {
		if r.Path == "SKILL.md" {
			count++
			assert.Equal(t, []byte("first"), r.Content, "first occurrence should win")
		}
	}
	assert.Equal(t, 1, count, "SKILL.md should appear once")
}

func TestLoadSkillResources_SkipsNonexistentPaths(t *testing.T) {
	resources := loadSkillResources([]string{"/nonexistent/path"})
	assert.Empty(t, resources)
}

func TestLoadSkillResources_EmptyPaths(t *testing.T) {
	resources := loadSkillResources(nil)
	assert.Empty(t, resources)
}

func TestIsTextFile(t *testing.T) {
	assert.True(t, isTextFile("SKILL.md"))
	assert.True(t, isTextFile("eval.yaml"))
	assert.True(t, isTextFile("check.py"))
	assert.True(t, isTextFile("script.sh"))
	assert.True(t, isTextFile("config.json"))
	assert.True(t, isTextFile("Makefile"))
	assert.True(t, isTextFile("Dockerfile"))
	assert.False(t, isTextFile("image.png"))
	assert.False(t, isTextFile("archive.zip"))
	assert.False(t, isTextFile("binary.exe"))
}
