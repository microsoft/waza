package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalNewCommand_GeneratesScaffoldFromSkill(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "deploy-assistant"), 0o755))

	skillMD := `---
name: deploy-assistant
description: |
  USE FOR: "deploy web applications", "roll back releases"
  DO NOT USE FOR: "write a poem"
---
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "deploy-assistant", "SKILL.md"), []byte(skillMD), 0o644))

	t.Chdir(dir)

	var out bytes.Buffer
	root := newRootCommand()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"eval", "new", "deploy-assistant"})
	require.NoError(t, root.Execute())

	evalPath := filepath.Join(dir, "evals", "deploy-assistant", "eval.yaml")
	assert.FileExists(t, evalPath)
	assert.FileExists(t, filepath.Join(dir, "evals", "deploy-assistant", "tasks", "positive-trigger-1.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "deploy-assistant", "tasks", "positive-trigger-2.yaml"))
	assert.FileExists(t, filepath.Join(dir, "evals", "deploy-assistant", "tasks", "negative-trigger-1.yaml"))

	evalData, err := os.ReadFile(evalPath)
	require.NoError(t, err)
	assert.Contains(t, string(evalData), "name: deploy-assistant-eval")
	assert.Contains(t, string(evalData), "type: behavior")
	assert.Contains(t, string(evalData), "max_tokens: 1200")
	require.Empty(t, validation.ValidateEvalBytes(evalData))

	positiveData, err := os.ReadFile(filepath.Join(dir, "evals", "deploy-assistant", "tasks", "positive-trigger-1.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(positiveData), "contains:")
	assert.Contains(t, string(positiveData), "deploy")
	require.Empty(t, validation.ValidateTaskBytes(positiveData))

	negativeData, err := os.ReadFile(filepath.Join(dir, "evals", "deploy-assistant", "tasks", "negative-trigger-1.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(negativeData), "not_contains:")
	assert.Contains(t, string(negativeData), "should_trigger: false")
	require.Empty(t, validation.ValidateTaskBytes(negativeData))
}

func TestEvalNewCommand_CustomOutputPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "my-skill"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "my-skill", "SKILL.md"), []byte(`---
name: my-skill
description: |
  USE FOR: "analyze logs"
  DO NOT USE FOR: "plan a vacation"
---
`), 0o644))

	t.Chdir(dir)

	customEvalPath := filepath.Join("custom", "evals", "my-skill", "eval.yaml")

	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"eval", "new", "my-skill", "--output", customEvalPath})
	require.NoError(t, root.Execute())

	assert.FileExists(t, filepath.Join(dir, customEvalPath))
	assert.FileExists(t, filepath.Join(dir, "custom", "evals", "my-skill", "tasks", "positive-trigger-1.yaml"))
}

func TestEvalNewCommand_MissingSkillMDError(t *testing.T) {
	dir := t.TempDir()

	t.Chdir(dir)

	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"eval", "new", "missing-skill"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SKILL.md not found")
}

func TestRootCommand_HasEvalSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "eval" {
			found = true
			assert.Contains(t, c.Use, "eval")
			break
		}
	}
	assert.True(t, found, "root command should have 'eval' subcommand")
}

func TestEvalNewCommand_RespectsCustomSkillsDirFromConfig(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: custom-skills/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "custom-skills", "deploy-assistant"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "custom-skills", "deploy-assistant", "SKILL.md"), []byte(`---
name: deploy-assistant
description: |
  USE FOR: "deploy web applications"
  DO NOT USE FOR: "write a poem"
---
`), 0o644))

	t.Chdir(dir)

	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"eval", "new", "deploy-assistant"})
	require.NoError(t, root.Execute())

	assert.FileExists(t, filepath.Join(dir, "evals", "deploy-assistant", "eval.yaml"))
}
