package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const cacheOutputTask = `id: test-task-001
name: Test Task
inputs:
  prompt: "Explain this code"
`

// writeSkillRootEval writes a skill whose eval uses the skill directory itself
// as its skill root, so run outputs written there sit beneath the fingerprint.
func writeSkillRootEval(t *testing.T, dir, name string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "task.yaml"), []byte(cacheOutputTask), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: \"A test skill description.\"\n---\n# Body\n"), 0o644))
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(`name: test-eval
skill: `+name+`
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
	return specPath
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
	singleSkill := func(extra func(dir string) []string) func(t *testing.T) []string {
		return func(t *testing.T) []string {
			dir := t.TempDir()
			return append([]string{writeSkillRootEval(t, dir, "test-skill")}, extra(dir)...)
		}
	}
	cases := map[string]func(t *testing.T) []string{
		"output-dir": singleSkill(func(dir string) []string {
			return []string{"--output-dir", filepath.Join(dir, "results")}
		}),
		"nested output-dir created by the run": singleSkill(func(dir string) []string {
			return []string{"--output-dir", filepath.Join(dir, "artifacts", "results")}
		}),
		"session-log": singleSkill(func(dir string) []string {
			return []string{"--session-log", "--session-dir", dir}
		}),
		"multi-model output": singleSkill(func(dir string) []string {
			return []string{"--model", "model-a", "--model", "model-b", "--output", filepath.Join(dir, "results.json")}
		}),
		"multi-skill output": func(t *testing.T) []string {
			root := t.TempDir()
			skillOne := filepath.Join(root, "skills", "skill-one")
			writeSkillRootEval(t, skillOne, "skill-one")
			writeSkillRootEval(t, filepath.Join(root, "skills", "skill-two"), "skill-two")
			t.Chdir(root)
			return []string{"--output", filepath.Join(skillOne, "results.json")}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			args := setup(t)
			cacheDir := filepath.Join(t.TempDir(), "cache")
			run := func() {
				resetRunGlobals()
				cmd := newRunCommand()
				cmd.SetArgs(append(args, "--cache", "--cache-dir", cacheDir))
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
