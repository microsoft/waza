package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/adversarial"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdversarial_DefaultExitFnPath_OnWarnDoesNotExit(t *testing.T) {
	// With on_unsafe_outcome=warn we must NOT touch os.Exit even though the
	// mock engine will trip every pack task. Failing to honor that would
	// crash the test binary itself.
	resetRunGlobals()

	out := &bytes.Buffer{}
	cmd := newAdversarialCommand()
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--engine", "mock",
		"--packs", "prompt-injection",
		"--on-unsafe-outcome", "warn",
	})

	err := cmd.Execute()
	require.NoError(t, err)
	got := out.String()
	assert.Contains(t, got, "Adversarial summary")
	assert.Contains(t, got, "unsafe:     4")
	assert.Contains(t, got, "result:     ❌ unsafe outcomes detected")
}

func TestAdversarial_FailPolicy_CallsExitWithCode2(t *testing.T) {
	resetRunGlobals()

	var captured int
	prev := adversarialExitFn
	adversarialExitFn = func(code int) { captured = code }
	t.Cleanup(func() { adversarialExitFn = prev })

	cmd := newAdversarialCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--engine", "mock",
		"--packs", "scope-bypass",
		"--on-unsafe-outcome", "fail",
	})

	_ = cmd.Execute()
	assert.Equal(t, AdversarialExitUnsafe, captured,
		"adversarialExitFn should be invoked with AdversarialExitUnsafe")
}

func TestAdversarial_UnknownPack(t *testing.T) {
	resetRunGlobals()

	// Use the test exit hook to capture the config-error exit code instead
	// of letting os.Exit kill the test process.
	var captured int
	prev := adversarialExitFn
	adversarialExitFn = func(code int) { captured = code }
	t.Cleanup(func() { adversarialExitFn = prev })

	// resolveAdversarialConfig returns the error before runAdversarial
	// reaches the exit hook, so cover the resolution path directly.
	_, _, err := resolveAdversarialConfig(&adversarialOptions{packs: []string{"does-not-exist"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown adversarial pack")
	assert.Equal(t, 0, captured, "exit hook should not fire for resolution errors")
}

func TestAdversarial_ResolveFromSpec(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "eval.yaml")
	spec := `schemaVersion: "1.2"
name: example
skill: example-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
adversarial:
  packs:
    - scope-bypass
  on_unsafe_outcome: warn
`
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	cfg, base, err := resolveAdversarialConfig(&adversarialOptions{specPath: specPath})
	require.NoError(t, err)
	require.NotNil(t, base)
	require.NotNil(t, base.Adversarial)
	assert.Equal(t, []string{"scope-bypass"}, cfg.Packs)
	assert.Equal(t, models.AdversarialOnUnsafeOutcomeWarn, cfg.OnUnsafeOutcome)
}

func TestAdversarial_ResolveFromSpec_FlagsOverride(t *testing.T) {
	resetRunGlobals()

	dir := t.TempDir()
	specPath := filepath.Join(dir, "eval.yaml")
	spec := `schemaVersion: "1.2"
name: example
skill: example-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
adversarial:
  packs:
    - scope-bypass
  on_unsafe_outcome: warn
`
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0o644))

	cfg, _, err := resolveAdversarialConfig(&adversarialOptions{
		specPath: specPath,
		packs:    []string{"prompt-injection"},
		onUnsafe: "fail",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"prompt-injection"}, cfg.Packs)
	assert.Equal(t, models.AdversarialOnUnsafeOutcomeFail, cfg.OnUnsafeOutcome)
}

func TestAdversarial_DefaultPacks(t *testing.T) {
	cfg, _, err := resolveAdversarialConfig(&adversarialOptions{})
	require.NoError(t, err)
	assert.Equal(t, adversarial.ListPacks(), cfg.Packs)
	assert.Equal(t, models.AdversarialOnUnsafeOutcomeFail, cfg.OnUnsafeOutcome)
}

func TestAdversarial_OutputFlagWritesResultsJSON(t *testing.T) {
	resetRunGlobals()

	out := filepath.Join(t.TempDir(), "results.json")
	cmd := newAdversarialCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--engine", "mock",
		"--packs", "scope-bypass",
		"--on-unsafe-outcome", "warn",
		"--output", out,
	})

	require.NoError(t, cmd.Execute())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	body := string(b)
	assert.True(t, strings.Contains(body, "\"schemaVersion\""), "results.json should include schemaVersion")
	assert.True(t, strings.Contains(body, "scope-bypass"), "results.json should reference the executed pack tasks")
}

func TestInjectContextDir_RewritesEveryTask(t *testing.T) {
	// Round-trip through Extract + injectContextDir, then re-read the
	// task files and confirm context_dir is now an absolute path to the
	// pack's fixtures dir.
	p, err := adversarial.LoadPack("prompt-injection")
	require.NoError(t, err)
	dst := t.TempDir()
	root, err := p.Extract(dst)
	require.NoError(t, err)
	fixtures := filepath.Join(root, "fixtures")
	require.NoError(t, injectContextDir(root, fixtures))

	entries, err := os.ReadDir(filepath.Join(root, "tasks"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, "tasks", e.Name()))
		require.NoError(t, err)
		assert.Contains(t, string(b), fmt.Sprintf("context_dir: %q", fixtures))
	}
}
