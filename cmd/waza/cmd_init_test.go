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
	assert.Contains(t, content, "actions/setup-go@v5")
	assert.Contains(t, content, "waza run")
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
