package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/stretchr/testify/require"
)

func TestRunCommandForSpecExpandsLockedRemoteGraders(t *testing.T) {
	resetRunGlobals()
	dir := t.TempDir()
	cacheRoot := filepath.Join(dir, "cache")
	t.Setenv("WAZA_MODULE_CACHE", cacheRoot)

	ref := "example.com/acme/graders#factuality@v1.0.0"
	commit := "0123456789abcdef0123456789abcdef01234567"
	moduleDir := filepath.Join(cacheRoot, "example.com", "acme", "graders", commit)
	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "graders"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "waza.registry.yaml"), []byte(`schema_version: 1
module: example.com/acme/graders
exports:
  graders:
    factuality:
      path: graders/factuality.yaml
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "graders", "factuality.yaml"), []byte(`type: text
name: factuality
config:
  contains: ["mock response"]
`), 0o644))
	digest, err := registry.DigestDirectory(moduleDir)
	require.NoError(t, err)
	lock := models.NewLockfile()
	lock.UpsertGrader(models.LockfileGrader{
		Ref:    ref,
		Commit: commit,
		Digest: digest,
		URL:    "https://example.com/acme/graders.git",
	})
	require.NoError(t, models.WriteLockfile(filepath.Join(dir, models.LockfileName), lock))

	taskPath := filepath.Join(dir, "task.yaml")
	require.NoError(t, os.WriteFile(taskPath, []byte(`id: remote-ref-task
name: Remote Ref Task
prompt: Say mock response
`), 0o644))
	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(`name: remote-ref-eval
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 10
  executor: mock
  model: mock-model
tasks:
  - task.yaml
graders:
  - ref: `+ref+`
    name: remote_text
metrics: []
`), 0o644))

	contextDir = dir
	results, err := runCommandForSpec(nil, skillSpecPath{evalSpecPath: specPath}, nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NotNil(t, results[0].outcome)
	require.Len(t, results[0].outcome.TestOutcomes, 1)
	require.Contains(t, results[0].outcome.TestOutcomes[0].Runs[0].Validations, "remote_text")
}
