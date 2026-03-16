package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCommand_WorkspaceDetection_SingleSkill(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	// Create a skill directory with SKILL.md and eval.yaml
	skillContent := "---\nname: ws-run-skill\ndescription: \"test\"\n---\n# Body\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0o644))

	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	task := "id: t1\nname: Task One\ninputs:\n  prompt: \"test\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(task), 0o644))

	spec := `name: ws-run-eval
skill: ws-run-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(spec), 0o644))

	t.Chdir(dir)

	cmd := newRunCommand()
	cmd.SetArgs(nil) // no args — workspace detection
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "workspace detection should find eval.yaml and run it")
}

func TestRunCommand_WorkspaceDetection_BySkillName(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills", "my-skill")
	require.NoError(t, os.MkdirAll(skillsDir, 0o755))

	skillContent := "---\nname: my-skill\ndescription: \"test\"\n---\n# Body\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillsDir, "SKILL.md"), []byte(skillContent), 0o644))

	taskDir := filepath.Join(skillsDir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	task := "id: t1\nname: Task One\ninputs:\n  prompt: \"test\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(task), 0o644))

	spec := `name: my-eval
skill: my-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`
	require.NoError(t, os.WriteFile(filepath.Join(skillsDir, "eval.yaml"), []byte(spec), 0o644))

	t.Chdir(dir)

	cmd := newRunCommand()
	cmd.SetArgs([]string{"my-skill"}) // skill name, not a path
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "should find eval.yaml for the named skill")
}

func TestRunCommand_ExplicitPath_BackwardCompat(t *testing.T) {
	resetRunGlobals()

	specPath := createTestSpec(t, "mock")

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	assert.NoError(t, err, "explicit path should still work")
}

func TestRunCommand_NoArgsNoWorkspace_Errors(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	t.Chdir(dir)

	cmd := newRunCommand()
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no eval.yaml specified")
}

func TestResolveSpecPaths_ExplicitEvalYaml(t *testing.T) {
	paths, err := resolveSpecPaths([]string{"some/eval.yaml"})
	require.NoError(t, err)
	require.Len(t, paths, 1)
	assert.Equal(t, "some/eval.yaml", paths[0].evalSpecPath)
}

func TestResolveSpecPaths_MultiSkill(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")

	for _, name := range []string{"alpha", "beta"} {
		skillDir := filepath.Join(skillsDir, name)
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		content := "---\nname: " + name + "\ndescription: \"test\"\n---\n# Body\n"
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "eval.yaml"), []byte("name: "+name+"\n"), 0o644))
	}
	t.Chdir(dir)

	paths, err := resolveSpecPaths(nil)
	require.NoError(t, err)
	assert.Len(t, paths, 2)
}
