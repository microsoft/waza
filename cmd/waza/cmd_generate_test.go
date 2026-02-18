package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetGenerateGlobals zeroes the package-level flag vars so prior tests don't leak.
func resetGenerateGlobals() {
	generateOutputDir = ""
}

func TestGenerateCommand_RequiresArg(t *testing.T) {
	resetGenerateGlobals()
	cmd := newGenerateCommand()
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	assert.Error(t, err)
}

func TestGenerateCommand_MissingFile(t *testing.T) {
	resetGenerateGlobals()
	var buf bytes.Buffer
	cmd := newGenerateCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"/nonexistent/SKILL.md"})
	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse SKILL.md")
}

func TestGenerateCommand_ValidSkill(t *testing.T) {
	resetGenerateGlobals()
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	content := "---\nname: test-gen\ndescription: Test generate\n---\n\n# Skill\n"
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	outDir := filepath.Join(dir, "output")

	var buf bytes.Buffer
	cmd := newGenerateCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{skillPath, "--output-dir", outDir})
	err := cmd.Execute()
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(outDir, "eval.yaml"))
	assert.FileExists(t, filepath.Join(outDir, "tasks", "test-gen-basic.yaml"))
	assert.FileExists(t, filepath.Join(outDir, "fixtures", "sample.txt"))

	// Verify deprecation notice
	assert.Contains(t, buf.String(), "alias for 'waza new'")
}

func TestGenerateCommand_DefaultOutputDir(t *testing.T) {
	resetGenerateGlobals()
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	content := "---\nname: my-skill\ndescription: Test\n---\n\n# Skill\n"
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	// Change to temp dir so default output goes there
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer func() {
		if chErr := os.Chdir(origDir); chErr != nil {
			t.Logf("warning: failed to restore working directory: %v", chErr)
		}
	}()

	var buf bytes.Buffer
	cmd := newGenerateCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{skillPath})
	err = cmd.Execute()
	require.NoError(t, err)

	// Without --output-dir, generate now delegates to waza new (standalone mode)
	assert.DirExists(t, filepath.Join(dir, "my-skill"))
	assert.FileExists(t, filepath.Join(dir, "my-skill", "SKILL.md"))
	assert.FileExists(t, filepath.Join(dir, "my-skill", "evals", "eval.yaml"))

	// Verify deprecation notice
	assert.Contains(t, buf.String(), "alias for 'waza new'")
}

func TestGenerateCommand_DeprecationNotice(t *testing.T) {
	resetGenerateGlobals()
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	content := "---\nname: test-dep\ndescription: Test deprecation notice\n---\n\n# Skill\n"
	require.NoError(t, os.WriteFile(skillPath, []byte(content), 0644))

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(origDir) //nolint:errcheck // best-effort cleanup

	var buf bytes.Buffer
	cmd := newGenerateCommand()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{skillPath})
	require.NoError(t, cmd.Execute())

	assert.Contains(t, buf.String(), "Note: 'waza generate' is an alias for 'waza new'. Use 'waza new <name>' instead.")
}

func TestGenerateCommand_RegisteredInRoot(t *testing.T) {
	resetGenerateGlobals()
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "generate" {
			found = true
			break
		}
	}
	assert.True(t, found, "generate command should be registered in root")
}
