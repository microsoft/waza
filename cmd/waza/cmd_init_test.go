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
	assert.Contains(t, content, "results/")
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
	assert.Contains(t, content, "paths:")
	assert.Contains(t, content, "skills: skills/")
	assert.Contains(t, content, "evals: evals/")
	assert.Contains(t, content, "results: results/")
	// Verify commented-out advanced sections
	assert.Contains(t, content, "# cache:")
	assert.Contains(t, content, "# server:")
	assert.Contains(t, content, "# tokens:")
	assert.Contains(t, content, "# graders:")
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

func TestGenerateWazaConfig(t *testing.T) {
	content := generateWazaConfig("mock", "gpt-5", "my-skills/", "my-evals/", "output/")
	assert.Contains(t, content, "skills: my-skills/")
	assert.Contains(t, content, "evals: my-evals/")
	assert.Contains(t, content, "results: output/")
	assert.Contains(t, content, "engine: mock")
	assert.Contains(t, content, "model: gpt-5")
	assert.Contains(t, content, "# cache:")
	assert.Contains(t, content, "# server:")
	assert.Contains(t, content, "# dev:")
	assert.Contains(t, content, "# tokens:")
	assert.Contains(t, content, "# graders:")
	assert.Contains(t, content, "$schema")
}

// --- Path Detection Tests ---

func TestDetectPaths_SkillsAtNonStandardPath(t *testing.T) {
	dir := t.TempDir()

	// Create skills at a non-standard path: plugin/skills/my-skill/SKILL.md
	skillDir := filepath.Join(dir, "plugin", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Skill\n"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "plugin/skills/", dp.SkillsDir)
}

func TestDetectPaths_EvalsAtNonStandardPath(t *testing.T) {
	dir := t.TempDir()

	// Create evals at a non-standard path: tests/evals/my-skill/eval.yaml
	evalDir := filepath.Join(dir, "tests", "evals", "my-skill")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\n"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "tests/evals/", dp.EvalsDir)
}

func TestDetectPaths_ResultsDetected(t *testing.T) {
	dir := t.TempDir()

	// Create results at output/results.json
	outputDir := filepath.Join(dir, "output")
	require.NoError(t, os.MkdirAll(outputDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outputDir, "results.json"), []byte("{}"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "output/", dp.ResultsDir)
}

func TestDetectPaths_ResultsWithPrefix(t *testing.T) {
	dir := t.TempDir()

	// Create results with a prefixed name: data/eval-results.json
	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "eval-results.json"), []byte("{}"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "data/", dp.ResultsDir)
}

func TestDetectPaths_NothingFound(t *testing.T) {
	dir := t.TempDir()

	dp := detectPaths(dir)
	assert.Empty(t, dp.SkillsDir)
	assert.Empty(t, dp.EvalsDir)
	assert.Empty(t, dp.ResultsDir)
}

func TestDetectPaths_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()

	// Create a SKILL.md directly in a subdirectory (no grandchild)
	// This means grandparent would be the parent of dir itself → ".."
	skillDir := filepath.Join(dir, "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Skill\n"), 0o644))

	dp := detectPaths(dir)
	// Should NOT detect — the grandparent would be outside the project root
	assert.Empty(t, dp.SkillsDir)
}

func TestDetectPaths_RejectsNonEvalDirectory(t *testing.T) {
	dir := t.TempDir()

	// eval.yaml inside tests/azure-prepare/ should NOT be detected as evals root
	// because "tests" does not contain "eval" in its name
	testDir := filepath.Join(dir, "tests", "azure-prepare")
	require.NoError(t, os.MkdirAll(testDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(testDir, "eval.yaml"), []byte("name: test\n"), 0o644))

	dp := detectPaths(dir)
	assert.Empty(t, dp.EvalsDir, "tests/ should not be detected as evals root")
}

func TestDetectPaths_RejectsNonSkillDirectory(t *testing.T) {
	dir := t.TempDir()

	// SKILL.md inside docs/my-guide/ should NOT be detected as skills root
	// because "docs" does not contain "skill" in its name
	docsDir := filepath.Join(dir, "docs", "my-guide")
	require.NoError(t, os.MkdirAll(docsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(docsDir, "SKILL.md"), []byte("# Guide\n"), 0o644))

	dp := detectPaths(dir)
	assert.Empty(t, dp.SkillsDir, "docs/ should not be detected as skills root")
}

func TestDetectPaths_SkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()

	// Put skills inside a hidden directory — should be skipped
	hiddenDir := filepath.Join(dir, ".hidden", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(hiddenDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenDir, "SKILL.md"), []byte("# Skill\n"), 0o644))

	dp := detectPaths(dir)
	assert.Empty(t, dp.SkillsDir)
}

func TestDetectPaths_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()

	// Put skills inside node_modules — should be skipped
	nmDir := filepath.Join(dir, "node_modules", "some-pkg", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(nmDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nmDir, "SKILL.md"), []byte("# Skill\n"), 0o644))

	dp := detectPaths(dir)
	assert.Empty(t, dp.SkillsDir)
}

func TestDetectPaths_FirstMatchWins(t *testing.T) {
	dir := t.TempDir()

	// Create two skills directories — first found should win
	// "aaa" sorts before "zzz" so aaa/skills should be found first
	skill1 := filepath.Join(dir, "aaa", "skills", "s1")
	skill2 := filepath.Join(dir, "zzz", "skills", "s2")
	require.NoError(t, os.MkdirAll(skill1, 0o755))
	require.NoError(t, os.MkdirAll(skill2, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skill1, "SKILL.md"), []byte("# S1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skill2, "SKILL.md"), []byte("# S2\n"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "aaa/skills/", dp.SkillsDir)
}

func TestDetectPaths_AllThreeDetected(t *testing.T) {
	dir := t.TempDir()

	// Skills
	skillDir := filepath.Join(dir, "src", "skills", "analyzer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Analyzer\n"), 0o644))

	// Evals
	evalDir := filepath.Join(dir, "test", "evals", "analyzer")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\n"), 0o644))

	// Results
	resultsDir := filepath.Join(dir, "output")
	require.NoError(t, os.MkdirAll(resultsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(resultsDir, "results.json"), []byte("{}"), 0o644))

	dp := detectPaths(dir)
	assert.Equal(t, "src/skills/", dp.SkillsDir)
	assert.Equal(t, "test/evals/", dp.EvalsDir)
	assert.Equal(t, "output/", dp.ResultsDir)
}

// --- CLI Flag Tests ---

func TestInitCommand_SkillsDirFlag(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill", "--skills-dir", "my-skills/"})
	require.NoError(t, cmd.Execute())

	// Verify custom skills directory was created
	assert.DirExists(t, filepath.Join(dir, "my-skills"))

	// Verify .waza.yaml uses the custom path
	data, err := os.ReadFile(filepath.Join(dir, ".waza.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "skills: my-skills/")
}

func TestInitCommand_AllDirFlags(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill", "--skills-dir", "custom-skills/", "--evals-dir", "custom-evals/", "--results-dir", "custom-results/"})
	require.NoError(t, cmd.Execute())

	// Verify custom directories were created
	assert.DirExists(t, filepath.Join(dir, "custom-skills"))
	assert.DirExists(t, filepath.Join(dir, "custom-evals"))

	// Verify .waza.yaml uses custom paths
	data, err := os.ReadFile(filepath.Join(dir, ".waza.yaml"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "skills: custom-skills/")
	assert.Contains(t, content, "evals: custom-evals/")
	assert.Contains(t, content, "results: custom-results/")
}

func TestInitCommand_FlagOverridesDetection(t *testing.T) {
	dir := t.TempDir()

	// Set up skills at a detectable path
	skillDir := filepath.Join(dir, "plugin", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Skill\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	// Flag should override the detected path
	cmd.SetArgs([]string{dir, "--no-skill", "--skills-dir", "override-skills/"})
	require.NoError(t, cmd.Execute())

	// Verify the flag value was used, not the detected one
	data, err := os.ReadFile(filepath.Join(dir, ".waza.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "skills: override-skills/")
}

func TestInitCommand_DetectionPrePopulatesConfig(t *testing.T) {
	dir := t.TempDir()

	// Set up skills at a non-standard path
	skillDir := filepath.Join(dir, "src", "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ntype: utility\ndescription: |\n  USE FOR: testing\n---\n# Skill\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	// Verify .waza.yaml uses the detected path (forward-slash normalized)
	data, err := os.ReadFile(filepath.Join(dir, ".waza.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "skills: src/skills/")

	// Verify detection message was shown
	output := buf.String()
	assert.Contains(t, output, "Detected existing paths")
}

func TestInitCommand_DetectionShowsMessage(t *testing.T) {
	dir := t.TempDir()

	// Set up evals at non-standard path
	evalDir := filepath.Join(dir, "test", "evals", "my-eval")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\n"), 0o644))

	var buf bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())

	output := buf.String()
	assert.Contains(t, output, "Detected existing paths")
	assert.Contains(t, output, "Evals:")
}
