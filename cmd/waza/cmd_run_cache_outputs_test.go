package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// createSkillRootSpec writes a spec whose skill directory is the spec
// directory itself, so run outputs written there sit beneath the skill root.
func createSkillRootSpec(t *testing.T) (specPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "task.yaml"), []byte(`id: test-task-001
name: Test Task
inputs:
  prompt: "Explain this code"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: test-skill\n---\nbody\n"), 0o644))
	specPath = filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(`name: test-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
  executor: mock
  model: test-model
  skill_directories:
    - "."
tasks:
  - "tasks/*.yaml"
`), 0o644))
	return specPath, dir
}

func countCacheEntries(t *testing.T, cacheDir string) int {
	t.Helper()
	count := 0
	require.NoError(t, filepath.WalkDir(cacheDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	}))
	return count
}

func TestRunCacheHitsWhenRunOutputsLiveBeneathSkillRoot(t *testing.T) {
	cases := map[string]func(dir string) []string{
		"output-dir": func(dir string) []string {
			return []string{"--output-dir", filepath.Join(dir, "results")}
		},
		"session-log": func(dir string) []string {
			return []string{"--session-log", "--session-dir", dir}
		},
	}
	for name, extraArgs := range cases {
		t.Run(name, func(t *testing.T) {
			specPath, dir := createSkillRootSpec(t)
			cacheDir := filepath.Join(t.TempDir(), "cache")
			run := func() {
				resetRunGlobals()
				cmd := newRunCommand()
				cmd.SetArgs(append([]string{specPath, "--cache", "--cache-dir", cacheDir}, extraArgs(dir)...))
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				require.NoError(t, cmd.Execute())
			}

			run()
			first := countCacheEntries(t, cacheDir)
			require.Positive(t, first, "first run should populate the cache")
			run()
			require.Equal(t, first, countCacheEntries(t, cacheDir),
				"second unchanged run should hit the cache instead of storing a new key")
		})
	}
}
