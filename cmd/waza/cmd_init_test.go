package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitCommand_CreatesProjectStructure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-project")

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd.Execute())

	// Verify directories created
	assert.DirExists(t, filepath.Join(target, "skills"))
	assert.DirExists(t, filepath.Join(target, "evals"))

	// Verify files created
	assert.FileExists(t, filepath.Join(target, ".waza.yaml"))
	assert.FileExists(t, filepath.Join(target, ".github", "workflows", "eval.yml"))
	assert.FileExists(t, filepath.Join(target, ".gitignore"))
	assert.FileExists(t, filepath.Join(target, "README.md"))

	// Verify output mentions items and descriptions
	output := buf.String()
	assert.Contains(t, output, "Project created")
	assert.Contains(t, output, "skills")
	assert.Contains(t, output, "evals")
	assert.Contains(t, output, ".waza.yaml")
	assert.Contains(t, output, "CI pipeline")
	assert.Contains(t, output, ".gitignore")
	assert.Contains(t, output, "README.md")
	assert.Contains(t, output, "Skill definitions")
	assert.Contains(t, output, "Evaluation suites")
}

func TestInitCommand_Idempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-project")

	// Run init first time
	cmd1 := newInitCommand()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd1.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd1.Execute())

	// Run init second time — should succeed and report "exists"
	var buf bytes.Buffer
	cmd2 := newInitCommand()
	cmd2.SetOut(&buf)
	cmd2.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd2.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd2.Execute())

	output := buf.String()
	assert.Contains(t, output, "up to date")
}

func TestInitCommand_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-project")

	// Create the target directory and a custom README
	require.NoError(t, os.MkdirAll(target, 0o755))
	customContent := "# My Custom README\n"
	require.NoError(t, os.WriteFile(filepath.Join(target, "README.md"), []byte(customContent), 0o644))

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd.Execute())

	// Verify the custom README was NOT overwritten
	data, err := os.ReadFile(filepath.Join(target, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, customContent, string(data))
}

func TestInitCommand_DefaultDir(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() {
		os.Chdir(origDir) //nolint:errcheck // best-effort cleanup
	})

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{"--no-skill"})
	require.NoError(t, cmd.Execute())

	assert.DirExists(t, filepath.Join(dir, "skills"))
	assert.DirExists(t, filepath.Join(dir, "evals"))
	assert.FileExists(t, filepath.Join(dir, ".gitignore"))
}

func TestInitCommand_TooManyArgs(t *testing.T) {
	cmd := newInitCommand()
	cmd.SetArgs([]string{"a", "b"})
	err := cmd.Execute()
	assert.Error(t, err)
}

func TestInitCommand_NoSkillFlag(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	// With --no-skill, the skill-related files should not exist
	assert.NoDirExists(t, filepath.Join(dir, "skills", "my-skill"))
	// But project structure should exist
	assert.DirExists(t, filepath.Join(dir, "skills"))
	assert.DirExists(t, filepath.Join(dir, "evals"))
}

func TestInitCommand_SkillPromptSkip(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	// Accessible mode: select engine=1, select model=1, confirm skill=n
	cmd.SetIn(strings.NewReader("1\n1\nn\n"))
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	// Skill directories should NOT exist since user declined
	assert.NoDirExists(t, filepath.Join(dir, "skills", "my-skill"))
}

func TestInitCommand_SkillPromptCreatesSkill(t *testing.T) {
	dir := t.TempDir()

	// First run init with --no-skill to set up project structure
	cmd1 := newInitCommand()
	cmd1.SetOut(&bytes.Buffer{})
	cmd1.SetIn(strings.NewReader("1\n1\n"))
	cmd1.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd1.Execute())

	// Verify project structure exists
	assert.DirExists(t, filepath.Join(dir, "skills"))
	assert.DirExists(t, filepath.Join(dir, "evals"))

	// Then call newCommandE directly (what init calls internally)
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir) //nolint:errcheck // best-effort cleanup

	cmd2 := newNewCommand()
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetArgs([]string{"test-skill"})
	require.NoError(t, cmd2.Execute())

	assert.FileExists(t, filepath.Join(dir, "skills", "test-skill", "SKILL.md"))
	assert.FileExists(t, filepath.Join(dir, "evals", "test-skill", "eval.yaml"))
}

func TestInitCommand_CIWorkflowContent(t *testing.T) {
	dir := t.TempDir()

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "Run Skill Evaluations")
	assert.Contains(t, content, "actions/checkout@v4")
	assert.Contains(t, content, "Azure/setup-azd@v2")
	assert.Contains(t, content, "azd waza run")
	assert.Contains(t, content, "upload-artifact@v4")
}

func TestInitCommand_GitignoreContent(t *testing.T) {
	dir := t.TempDir()

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "results.json")
	assert.Contains(t, content, ".waza-cache/")
	assert.Contains(t, content, "coverage.txt")
	assert.Contains(t, content, "*.exe")
}

func TestInitCommand_ReadmeContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-project")

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(target, "README.md"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "# my-project")
	assert.Contains(t, content, "waza new my-skill")
	assert.Contains(t, content, "waza run")
	assert.Contains(t, content, "waza check")
	assert.Contains(t, content, "git push")
}

func TestInitCommand_WazaYAMLContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "my-project")

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader("1\n\nskip\n"))
	cmd.SetArgs([]string{target, "--no-skill"})
	require.NoError(t, cmd.Execute())

	data, err := os.ReadFile(filepath.Join(target, ".waza.yaml"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "engine: copilot-sdk")
	assert.Contains(t, content, "model: claude-sonnet-4.6")
	assert.Contains(t, content, "defaults:")
}

func TestInitCommand_InventoryDiscoversSkills(t *testing.T) {
	dir := t.TempDir()

	// Set up project structure with a skill but no eval
	skillDir := filepath.Join(dir, "skills", "my-analyzer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-analyzer
type: utility
description: |
  USE FOR: analysis
---

# My Analyzer
`), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	output := buf.String()
	assert.Contains(t, output, "1 skills found, 1 missing eval")
	assert.Contains(t, output, "Skill: my-analyzer")
}

func TestInitCommand_InventoryScaffoldsEvals(t *testing.T) {
	dir := t.TempDir()

	// Set up project with a skill missing its eval
	skillDir := filepath.Join(dir, "skills", "code-explainer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: code-explainer
type: utility
description: |
  USE FOR: explaining code
---

# Code Explainer
`), 0o644))

	// Pre-create .waza.yaml so the config prompt is skipped
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults:\n  engine: mock\n  model: gpt-5\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	output := buf.String()
	// Non-TTY auto-scaffolds evals for skills missing them
	assert.Contains(t, output, "Eval: code-explainer")

	// Verify eval files were created (uses scaffold package — same as waza new)
	assert.FileExists(t, filepath.Join(dir, "evals", "code-explainer", "eval.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "code-explainer", "tasks", "basic-usage.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "code-explainer", "fixtures", "sample.py"))
}

func TestInitCommand_InventorySkipsExistingEvals(t *testing.T) {
	dir := t.TempDir()

	// Set up skill WITH its eval already present
	skillDir := filepath.Join(dir, "skills", "summarizer")
	evalDir := filepath.Join(dir, "evals", "summarizer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: summarizer
type: utility
description: |
  USE FOR: summarizing
---

# Summarizer
`), 0o644))
	evalContent := "name: summarizer-eval\n"
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte(evalContent), 0o644))

	// Pre-create config
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults:\n  engine: mock\n  model: gpt-5\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	output := buf.String()
	assert.Contains(t, output, "1 skills found, 0 missing eval")
	assert.Contains(t, output, "Skill: summarizer")
	assert.Contains(t, output, "Eval: summarizer")

	// Verify eval was NOT overwritten
	data, err := os.ReadFile(filepath.Join(evalDir, "eval.yaml"))
	require.NoError(t, err)
	assert.Equal(t, evalContent, string(data))
}

func TestInitCommand_InventoryMixedSkills(t *testing.T) {
	dir := t.TempDir()

	// Skill A: has eval
	skillDirA := filepath.Join(dir, "skills", "alpha")
	evalDirA := filepath.Join(dir, "evals", "alpha")
	require.NoError(t, os.MkdirAll(skillDirA, 0o755))
	require.NoError(t, os.MkdirAll(evalDirA, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDirA, "SKILL.md"), []byte("---\nname: alpha\ntype: utility\ndescription: |\n  USE FOR: alpha\n---\n# Alpha\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(evalDirA, "eval.yaml"), []byte("name: alpha-eval\n"), 0o644))

	// Skill B: missing eval
	skillDirB := filepath.Join(dir, "skills", "beta")
	require.NoError(t, os.MkdirAll(skillDirB, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDirB, "SKILL.md"), []byte("---\nname: beta\ntype: utility\ndescription: |\n  USE FOR: beta\n---\n# Beta\n"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults:\n  engine: copilot-sdk\n  model: claude-sonnet-4.6\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	output := buf.String()
	assert.Contains(t, output, "2 skills found, 1 missing eval")

	// Beta's eval should be scaffolded
	assert.FileExists(t, filepath.Join(dir, "evals", "beta", "eval.yaml"))

	// Alpha's eval should be untouched
	data, err := os.ReadFile(filepath.Join(evalDirA, "eval.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "name: alpha-eval\n", string(data))
}

func TestInitCommand_ScaffoldedEvalContent(t *testing.T) {
	dir := t.TempDir()

	// Set up skill without eval, with specific engine/model config
	skillDir := filepath.Join(dir, "skills", "test-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "evals"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: test-skill\ntype: utility\ndescription: |\n  USE FOR: testing\n---\n# Test\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults:\n  engine: mock\n  model: gpt-5\n"), 0o644))

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	// Verify eval.yaml content uses values from existing .waza.yaml
	data, err := os.ReadFile(filepath.Join(dir, "evals", "test-skill", "eval.yaml"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "name: test-skill-eval")
	assert.Contains(t, content, "skill: test-skill")
	assert.Contains(t, content, "executor: mock")
	assert.Contains(t, content, "model: gpt-5")
}

func TestRootCommand_HasInitSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "init" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'init' subcommand")
}
