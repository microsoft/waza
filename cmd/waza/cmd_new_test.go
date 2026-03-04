package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/scaffold"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── In-Project Mode Tests ──────────────────────────────────────────────────────

func TestNewCommand_InProjectMode(t *testing.T) {
	// Set up a temp dir with a skills/ directory to trigger in-project mode
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// Verify all expected files exist
	expectedFiles := []string{
		filepath.Join(dir, "skills", "my-skill", "SKILL.md"),
		filepath.Join(dir, "evals", "my-skill", "eval.yaml"),
		filepath.Join(dir, "evals", "my-skill", "tasks", "basic-usage.yaml"),
		filepath.Join(dir, "evals", "my-skill", "tasks", "edge-case.yaml"),
		filepath.Join(dir, "evals", "my-skill", "tasks", "should-not-trigger.yaml"),
		filepath.Join(dir, "evals", "my-skill", "fixtures", "sample.py"),
	}
	for _, f := range expectedFiles {
		assert.FileExists(t, f, "expected file: %s", f)
	}

	// Verify project-level files are NOT created in-project mode
	notExpected := []string{
		filepath.Join(dir, "skills", "my-skill", ".github"),
		filepath.Join(dir, "skills", "my-skill", ".gitignore"),
		filepath.Join(dir, "skills", "my-skill", "README.md"),
		filepath.Join(dir, "evals", "my-skill", ".github"),
		filepath.Join(dir, "evals", "my-skill", ".gitignore"),
		filepath.Join(dir, "evals", "my-skill", "README.md"),
	}
	for _, f := range notExpected {
		assert.NoFileExists(t, f, "should not exist in-project mode: %s", f)
	}

	output := buf.String()
	assert.Contains(t, output, "SKILL.md")
	assert.Contains(t, output, "eval.yaml")
}

// ── Standalone Mode Tests ──────────────────────────────────────────────────────

func TestNewCommand_StandaloneMode(t *testing.T) {
	// No skills/ directory → standalone mode
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// Verify all expected files — standalone creates everything under {name}/
	expectedFiles := []string{
		filepath.Join(dir, "my-skill", "SKILL.md"),
		filepath.Join(dir, "my-skill", "evals", "eval.yaml"),
		filepath.Join(dir, "my-skill", "evals", "tasks", "basic-usage.yaml"),
		filepath.Join(dir, "my-skill", "evals", "tasks", "edge-case.yaml"),
		filepath.Join(dir, "my-skill", "evals", "tasks", "should-not-trigger.yaml"),
		filepath.Join(dir, "my-skill", "evals", "fixtures", "sample.py"),
		filepath.Join(dir, "my-skill", ".github", "workflows", "eval.yml"),
		filepath.Join(dir, "my-skill", ".gitignore"),
		filepath.Join(dir, "my-skill", "README.md"),
	}
	for _, f := range expectedFiles {
		assert.FileExists(t, f, "expected file: %s", f)
	}

	output := buf.String()
	assert.Contains(t, output, "SKILL.md")
	assert.Contains(t, output, "eval.yml")
	assert.Contains(t, output, "README.md")
}

// ── No-Overwrite Safety Tests ──────────────────────────────────────────────────

func TestNewCommand_NoOverwriteSafety(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create SKILL.md with valid frontmatter — this should NOT be overwritten
	skillDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	customContent := "---\nname: my-skill\ndescription: My custom skill\n---\n\n# My Custom Skill\nDo not overwrite me."
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(customContent), 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be unchanged (valid frontmatter → preserved)
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, customContent, string(data), "valid SKILL.md should not be overwritten")

	// Output should mention ✅ for skipped file
	assert.Contains(t, buf.String(), "✅")
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "eval.yaml"))
}

// ── Idempotent Behavior Tests ──────────────────────────────────────────────────

func TestNewCommand_IdempotentWithExistingSkillMD(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create SKILL.md with valid frontmatter
	skillDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	validSkillMD := "---\nname: my-skill\ndescription: A test skill\n---\n\n# My Skill\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(validSkillMD), 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be unchanged (idempotent — detected and skipped)
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, validSkillMD, string(data), "existing SKILL.md should not be overwritten")

	// Output should mention ✅ for skipped file
	assert.Contains(t, buf.String(), "✅")

	// Eval files should be created
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "eval.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "tasks", "basic-usage.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "fixtures", "sample.py"))
}

func TestNewCommand_IdempotentRunTwice(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// First run
	cmd1 := newNewCommand()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd1.Execute())

	// Second run should succeed (idempotent, skip all existing)
	var buf bytes.Buffer
	cmd2 := newNewCommand()
	cmd2.SetOut(&buf)
	cmd2.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd2.Execute())

	output := buf.String()
	assert.Contains(t, output, "✅")
	assert.Contains(t, output, "Project up to date")
}

// ── SKILL.md Status Variation Tests ────────────────────────────────────────────

func TestNewCommand_EmptySkillMD_NonTTY_OverwritesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create an empty SKILL.md (0 bytes)
	skillDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte{}, 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be overwritten with default content (not empty anymore)
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.NotEmpty(t, string(data), "empty SKILL.md should be overwritten with defaults")
	assert.Contains(t, string(data), "name: my-skill", "overwritten SKILL.md should have valid frontmatter")

	// Warning should appear in output
	output := buf.String()
	assert.Contains(t, output, "empty or malformed")
	assert.Contains(t, output, "➕")

	// Other eval files should still be created
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "eval.yaml"))
}

func TestNewCommand_MalformedSkillMD_NonTTY_OverwritesWithDefaults(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create a SKILL.md with invalid content (no frontmatter)
	skillDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("just some text, no frontmatter"), 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be overwritten
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: my-skill", "malformed SKILL.md should be overwritten")

	// Warning should appear
	assert.Contains(t, buf.String(), "empty or malformed")
}

func TestNewCommand_ValidSkillMD_NonTTY_SkipsToInventory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create a valid SKILL.md
	skillDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	validContent := "---\nname: my-skill\ndescription: A valid skill\n---\n\n# My Skill\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(validContent), 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should NOT be changed
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, validContent, string(data), "valid SKILL.md should not be modified")

	// Output should show "Checking" header (not "Scaffolding") and ✓ for SKILL.md
	output := buf.String()
	assert.Contains(t, output, "Checking skill")
	assert.NotContains(t, output, "empty or malformed")

	// Eval files should be created
	assert.FileExists(t, filepath.Join(dir, "evals", "my-skill", "eval.yaml"))
}

func TestNewCommand_NoSkillMD_NonTTY_CreatesEverything(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be created with default content
	data, err := os.ReadFile(filepath.Join(dir, "skills", "my-skill", "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: my-skill")

	// Output should show "Scaffolding" header
	output := buf.String()
	assert.Contains(t, output, "Scaffolding skill")
	assert.Contains(t, output, "Skill created")
}

func TestNewCommand_EmptySkillMD_EvalFilesPreExist(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// Pre-create empty SKILL.md AND some eval files
	skillDir := filepath.Join(dir, "skills", "my-skill")
	evalDir := filepath.Join(dir, "evals", "my-skill")
	tasksDir := filepath.Join(evalDir, "tasks")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte{}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("existing eval"), 0o644))

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	// SKILL.md should be overwritten
	data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "name: my-skill")

	// eval.yaml should NOT be overwritten (it's not marked for overwrite)
	evalData, err := os.ReadFile(filepath.Join(evalDir, "eval.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "existing eval", string(evalData), "eval.yaml should not be overwritten")

	// Output should show ➕ for repaired SKILL.md, ✅ for existing eval.yaml
	output := buf.String()
	assert.Contains(t, output, "➕")
}

// ── Name Validation Tests ──────────────────────────────────────────────────────

func TestNewCommand_NameValidation(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "valid kebab-case name",
			args:      []string{"my-skill"},
			wantError: false,
		},
		{
			name:      "valid simple name",
			args:      []string{"skill"},
			wantError: false,
		},
		{
			name:      "no argument",
			args:      []string{},
			wantError: true,
		},
		{
			name:      "path traversal with dots",
			args:      []string{"../evil"},
			wantError: true,
			errorMsg:  "invalid path characters",
		},
		{
			name:      "path traversal with forward slash",
			args:      []string{"a/b"},
			wantError: true,
			errorMsg:  "invalid path characters",
		},
		{
			name:      "path traversal with backslash",
			args:      []string{"a\\b"},
			wantError: true,
			errorMsg:  "invalid path characters",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			origDir, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(dir))
			t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

			cmd := newNewCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			err = cmd.Execute()

			if tc.wantError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// ── Content Validation Tests ───────────────────────────────────────────────────

func TestNewCommand_EvalYAMLContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"code-analyzer"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "evals", "code-analyzer", "eval.yaml"))
	require.NoError(t, err)
	content := string(data)

	// Verify skill name is embedded correctly
	assert.Contains(t, content, "name: code-analyzer-eval")
	assert.Contains(t, content, "skill: code-analyzer")

	// Verify 2 grader types (no behavior — requires real sessions)
	assert.Contains(t, content, "type: code")
	assert.Contains(t, content, "type: text")
	assert.NotContains(t, content, "type: behavior")

	// Verify default engine is copilot-sdk (not mock)
	assert.Contains(t, content, "executor: copilot-sdk")
	assert.NotContains(t, content, "executor: mock")

	// Verify task glob
	assert.Contains(t, content, `"tasks/*.yaml"`)
}

func TestNewCommand_WazaYAMLDefaults(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	// Write .waza.yaml with custom defaults
	wazaConfig := "defaults:\n  engine: mock\n  model: claude-sonnet\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(wazaConfig), 0o644))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir)) //nolint:errcheck
	defer os.Chdir(origDir)           //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "evals", "my-skill", "eval.yaml"))
	require.NoError(t, err)
	content := string(data)

	// Verify .waza.yaml defaults were applied
	assert.Contains(t, content, "executor: mock")
	assert.Contains(t, content, "model: claude-sonnet")
}

func TestNewCommand_SkillMDContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"code-analyzer"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "skills", "code-analyzer", "SKILL.md"))
	require.NoError(t, err)
	content := string(data)

	// Verify frontmatter has correct name
	assert.Contains(t, content, "name: code-analyzer")
	// Verify title case heading
	assert.Contains(t, content, "# Code Analyzer")
	// Verify USE FOR / DO NOT USE FOR scaffold
	assert.Contains(t, content, "USE FOR:")
	assert.Contains(t, content, "DO NOT USE FOR:")
}

func TestNewCommand_TaskFileIDs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	tasksDir := filepath.Join(dir, "evals", "my-skill", "tasks")

	tests := []struct {
		file string
		id   string
	}{
		{"basic-usage.yaml", "id: basic-usage-001"},
		{"edge-case.yaml", "id: edge-case-001"},
		{"should-not-trigger.yaml", "id: should-not-trigger-001"},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(tasksDir, tc.file))
			require.NoError(t, err)
			assert.Contains(t, string(data), tc.id)
		})
	}
}

// ── Interactive Flag Tests ─────────────────────────────────────────────────────

func TestNewCommand_InteractiveFlagRemoved(t *testing.T) {
	// Verify --interactive flag is no longer accepted
	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--interactive", "test-skill"})
	err := cmd.Execute()
	assert.Error(t, err, "--interactive flag should no longer be accepted")
}

// ── Template Flag Tests ────────────────────────────────────────────────────────

func TestNewCommand_TemplateFlagNote(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--template", "fancy", "my-skill"})
	require.NoError(t, cmd.Execute())

	assert.Contains(t, buf.String(), "template packs coming soon")
}

// ── Root Command Registration Test ─────────────────────────────────────────────

func TestRootCommand_HasNewSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "new" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'new' subcommand")
}

// ── CI Workflow Content (Standalone Only) ──────────────────────────────────────

func TestNewCommand_StandaloneCIWorkflowContent(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"code-analyzer"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "code-analyzer", ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "name: Eval Code Analyzer")
	assert.Contains(t, content, "waza run")
}

func TestNewCommand_StandaloneGitignoreContent(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "my-skill", ".gitignore"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "results.json")
	assert.Contains(t, content, ".waza-cache/")
}

func TestNewCommand_StandaloneReadmeContent(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "my-skill", "README.md"))
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "# My Skill")
	assert.Contains(t, content, "waza run")
}

// ── Fixture Content Test ───────────────────────────────────────────────────────

func TestNewCommand_FixtureContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	cmd := newNewCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"my-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, "evals", "my-skill", "fixtures", "sample.py"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "def hello(name):")
}

// ── Title Case Helper Test ─────────────────────────────────────────────────────

func TestTitleCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-skill", "My Skill"},
		{"code-analyzer", "Code Analyzer"},
		{"skill", "Skill"},
		{"a-b-c", "A B C"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, scaffold.TitleCase(tc.input))
		})
	}
}

// ── Generate Alias Tests ───────────────────────────────────────────────────────

func TestNewCommand_GenerateAlias(t *testing.T) {
	root := newRootCommand()
	for _, c := range root.Commands() {
		if c.Name() == "new" {
			assert.Contains(t, c.Aliases, "generate", "'new' command should have 'generate' alias")
			return
		}
	}
	t.Fatal("'new' command not found in root")
}

// ── Output Dir Flag Tests ──────────────────────────────────────────────────────

func TestNewCommand_OutputDirFlag(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "output")

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newNewCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"my-skill", "--output-dir", outDir})
	require.NoError(t, cmd.Execute())

	// Files should be created inside outDir (standalone mode since no skills/)
	assert.FileExists(t, filepath.Join(outDir, "my-skill", "SKILL.md"))
	assert.FileExists(t, filepath.Join(outDir, "my-skill", "evals", "eval.yaml"))
	assert.FileExists(t, filepath.Join(outDir, "my-skill", "evals", "tasks", "basic-usage.yaml"))
}
