package suggest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Focus category tests ---

func TestValidateFocusAcceptsKnown(t *testing.T) {
	for _, f := range AvailableFocusCategories() {
		require.NoError(t, ValidateFocus(f), "expected %s to be accepted", f)
	}
	require.NoError(t, ValidateFocus(""), "empty focus should be allowed")
	require.NoError(t, ValidateFocus(" negative-triggers "), "focus should be normalized before validation")
}

func TestValidateFocusRejectsUnknown(t *testing.T) {
	err := ValidateFocus("not-a-real-category")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not-a-real-category")
}

func TestRenderImplementationPromptIncludesFocusDirective(t *testing.T) {
	cases := map[FocusCategory]string{
		FocusTriggers:         "positive trigger phrases",
		FocusNegativeTriggers: "should NOT",
		FocusEdgeFixtures:     "edge cases",
		FocusDoNotUseFor:      "DO NOT USE FOR",
		FocusParameters:       "vary the parameters",
	}
	for focus, marker := range cases {
		t.Run(string(focus), func(t *testing.T) {
			prompt := renderImplementationPrompt(promptData{
				SkillName: "sample-skill",
				Focus:     string(focus),
			}, "")
			require.Contains(t, prompt, marker, "directive marker missing for %s", focus)
		})
	}
}

func TestRenderImplementationPromptHonorsCount(t *testing.T) {
	prompt := renderImplementationPrompt(promptData{
		SkillName: "sample-skill",
		Count:     5,
	}, "")
	require.Contains(t, prompt, "EXACTLY 5 tasks")
}

func TestRenderImplementationPromptDefaultGuidance(t *testing.T) {
	prompt := renderImplementationPrompt(promptData{SkillName: "sample-skill"}, "")
	require.Contains(t, prompt, "at least 3 diverse tasks")
}

func TestRenderImplementationPromptRequiresConfidenceAndRationale(t *testing.T) {
	prompt := renderImplementationPrompt(promptData{SkillName: "sample-skill"}, "")
	require.Contains(t, prompt, "confidence")
	require.Contains(t, prompt, "rationale")
}

// --- Overwrite-safety tests ---

func minimalSuggestion() *Suggestion {
	return &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{
				Path:       "tasks/task-01.yaml",
				Content:    "id: task-01\nname: Task One\ninputs:\n  prompt: hi\n",
				Confidence: 0.8,
				Rationale:  "matches USE FOR: summarize",
			},
		},
		Fixtures: []GeneratedFile{
			{Path: "fixtures/sample.txt", Content: "data"},
		},
	}
}

func TestWriteToDirSkipsExistingEvalYAML(t *testing.T) {
	dir := t.TempDir()
	existing := "name: curated\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(existing), 0o644))

	s := minimalSuggestion()
	written, err := s.WriteToDir(dir, WriteOptions{})
	require.NoError(t, err)

	// eval.yaml should not be in written (skipped because it exists),
	// but the new task + fixture should be written.
	for _, p := range written {
		assert.NotEqual(t, "eval.yaml", filepath.Base(p), "should not have rewritten eval.yaml")
	}
	require.Len(t, written, 2)

	// eval.yaml content untouched
	raw, err := os.ReadFile(filepath.Join(dir, "eval.yaml"))
	require.NoError(t, err)
	require.Equal(t, existing, string(raw))
}

func TestWriteToDirRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "tasks", "task-01.yaml"),
		[]byte("id: other\nname: Other\ninputs:\n  prompt: x\n"),
		0o644,
	))

	s := minimalSuggestion()
	_, err := s.WriteToDir(dir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "refusing to overwrite")
	require.Contains(t, err.Error(), "diff:")
	require.Contains(t, err.Error(), "--- tasks/task-01.yaml (existing)")
	require.Contains(t, err.Error(), "+++ tasks/task-01.yaml (suggested)")
	require.Contains(t, err.Error(), "-id: other")
	require.Contains(t, err.Error(), "+id: task-01")
}

func TestWriteToDirAllowsOverwriteWithForce(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	existingTask := filepath.Join(dir, "tasks", "task-01.yaml")
	require.NoError(t, os.WriteFile(existingTask, []byte("id: stale\nname: Stale\ninputs:\n  prompt: x\n"), 0o644))

	s := minimalSuggestion()
	written, err := s.WriteToDir(dir, WriteOptions{Force: true})
	require.NoError(t, err)
	require.NotEmpty(t, written)

	raw, err := os.ReadFile(existingTask)
	require.NoError(t, err)
	require.Contains(t, string(raw), "id: task-01")
}

func TestWriteToDirUsesConfiguredEvalAndTaskNames(t *testing.T) {
	dir := t.TempDir()
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Content: "id: custom-001\nname: Custom\ninputs:\n  prompt: hi\n", Confidence: 0.7, Rationale: "matches USE FOR"},
		},
	}

	written, err := s.WriteToDir(dir, WriteOptions{
		EvalFile:       "waza-eval.yaml",
		TaskGlob:       "cases/*.waza-task.yaml",
		TaskFileSuffix: ".waza-task.yaml",
	})
	require.NoError(t, err)
	require.Len(t, written, 2)
	require.FileExists(t, filepath.Join(dir, "waza-eval.yaml"))
	require.FileExists(t, filepath.Join(dir, "cases", "task-01.waza-task.yaml"))
	require.NoFileExists(t, filepath.Join(dir, "eval.yaml"))
}

func TestWriteToDirDetectsDuplicateTaskIDAgainstExisting(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	// Existing task with id "task-01" at a *different* file path —
	// path doesn't collide, but the id does.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "tasks", "previous.yaml"),
		[]byte("id: task-01\nname: Previous\ninputs:\n  prompt: x\n"),
		0o644,
	))

	s := minimalSuggestion()
	_, err := s.WriteToDir(dir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "task-01")
	require.Contains(t, strings.ToLower(err.Error()), "id")
	require.Contains(t, err.Error(), "diff:")
	require.Contains(t, err.Error(), "--- tasks/previous.yaml (existing)")
	require.Contains(t, err.Error(), "+name: Task One")
}

func TestWriteToDirErrorsOnUnreadableExistingTask(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks", "broken.yaml"), 0o755))

	s := minimalSuggestion()
	_, err := s.WriteToDir(dir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading existing task")
	require.Contains(t, err.Error(), "collision check")
}

func TestWriteToDirReportsDuplicateExistingTaskIDs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "tasks", "a.yaml"),
		[]byte("id: duplicated\nname: A\ninputs:\n  prompt: a\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "tasks", "b.yaml"),
		[]byte("id: duplicated\nname: B\ninputs:\n  prompt: b\n"),
		0o644,
	))

	s := minimalSuggestion()
	_, err := s.WriteToDir(dir, WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "existing tasks contain duplicate id(s)")
	require.Contains(t, err.Error(), "duplicated")
	require.Contains(t, err.Error(), "tasks/a.yaml")
	require.Contains(t, err.Error(), "tasks/b.yaml")
}

func TestWriteToDirRejectsDuplicateIDsWithinBatch(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "tasks/a.yaml", Content: "id: dup\nname: A\ninputs:\n  prompt: hi\n", Confidence: 0.6, Rationale: "matches USE FOR"},
			{Path: "tasks/b.yaml", Content: "id: dup\nname: B\ninputs:\n  prompt: hi\n", Confidence: 0.6, Rationale: "matches USE FOR"},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate id")
}

// --- Schema validation tests ---

func TestWriteToDirRejectsTaskMissingID(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			// no id field
			{Path: "tasks/bad.yaml", Content: "name: Bad\ninputs:\n  prompt: hi\n", Confidence: 0.6, Rationale: "matches USE FOR"},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "schema")
}

func TestWriteToDirRejectsTaskMissingInputs(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "tasks/bad.yaml", Content: "id: missing-inputs\nname: Bad\n", Confidence: 0.6, Rationale: "matches USE FOR"},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "schema")
}

func TestWriteToDirRejectsTaskWithUnknownField(t *testing.T) {
	// task.schema.json has additionalProperties: false
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "tasks/bad.yaml", Content: "id: x\nname: X\ninputs:\n  prompt: hi\nconfidence: 0.9\n", Confidence: 0.6, Rationale: "matches USE FOR"},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
}

func TestWriteToDirRejectsTaskWithInvalidConfidence(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "tasks/bad.yaml", Content: "id: x\nname: X\ninputs:\n  prompt: hi\n", Confidence: 1.2, Rationale: "matches USE FOR"},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid confidence")
}

func TestWriteToDirRejectsTaskMissingRationale(t *testing.T) {
	s := &Suggestion{
		EvalYAML: validEvalYAML(),
		Tasks: []GeneratedFile{
			{Path: "tasks/bad.yaml", Content: "id: x\nname: X\ninputs:\n  prompt: hi\n", Confidence: 0.5},
		},
	}
	_, err := s.WriteToDir(t.TempDir(), WriteOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rationale")
}
