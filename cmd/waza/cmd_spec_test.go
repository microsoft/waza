package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommandHasSpecSubcommand(t *testing.T) {
	root := newRootCommand()
	var found bool
	for _, cmd := range root.Commands() {
		if cmd.Name() == "spec" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have spec subcommand")
}

func TestSpecVerifyCommandJSON(t *testing.T) {
	root := t.TempDir()
	skillPath, evalPath := writeSpecVerifyFixture(t, root, true)

	cmd := newSpecVerifyCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{skillPath, evalPath, "--format", "json"})

	require.NoError(t, cmd.Execute())

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
	summary, ok := decoded["summary"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), summary["uncovered_requirements"])
}

func TestSpecVerifyCommandFailMode(t *testing.T) {
	root := t.TempDir()
	skillPath, evalPath := writeSpecVerifyFixture(t, root, false)

	cmd := newSpecVerifyCommand()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{skillPath, evalPath, "--fail", "--threshold", "1"})

	err := cmd.Execute()
	require.Error(t, err)
	var testFailure *TestFailureError
	assert.True(t, errors.As(err, &testFailure))
	assert.Contains(t, err.Error(), "spec verify failed")
}

func writeSpecVerifyFixture(t *testing.T, root string, includeNegative bool) (string, string) {
	t.Helper()
	skillPath := filepath.Join(root, "SKILL.md")
	evalPath := filepath.Join(root, "eval.yaml")
	tasksDir := filepath.Join(root, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))

	require.NoError(t, os.WriteFile(skillPath, []byte(`---
name: pr-summarizer
description: |
  Summarize PR diffs.
  USE FOR: summarize a PR diff.
  DO NOT USE FOR: code review security PRs.
---
`), 0o644))
	require.NoError(t, os.WriteFile(evalPath, []byte(`name: pr-summarizer-eval
skill: pr-summarizer
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  parallel: false
  executor: mock
  model: mock
graders:
  - type: text
    name: basic
metrics:
  - name: coverage
    weight: 1
    threshold: 1
tasks:
  - tasks/*.yaml
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "use.yaml"), []byte(`id: pr-summary-basic
name: PR summary basic
description: Summarize a PR diff.
inputs:
  prompt: Please summarize this PR diff.
expected:
  should_trigger: true
`), 0o644))
	if includeNegative {
		require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "dont.yaml"), []byte(`id: security-review-negative
name: Security review negative trigger
description: Code review security PRs should not trigger this skill.
inputs:
  prompt: Please do code review security PRs.
expected:
  should_trigger: false
`), 0o644))
	}
	return skillPath, evalPath
}
