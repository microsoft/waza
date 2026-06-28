package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/specverify"
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

func TestSpecVerifyCommandGitHubActionsHonorsWarnFalse(t *testing.T) {
	root := t.TempDir()
	skillPath, evalPath := writeSpecVerifyFixture(t, root, false)

	cmd := newSpecVerifyCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{skillPath, evalPath, "--format", "github-actions", "--warn=false"})

	require.NoError(t, cmd.Execute())
	assert.Empty(t, out.String())
}

func TestSpecVerifyCommandGitHubActionsUsesRelativePathAndMessageEscaping(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	skillPath := filepath.Join(root, "skills", "example", "SKILL.md")

	report := &specverify.Report{
		Coverage: []specverify.RequirementCoverage{{
			Requirement: specverify.Requirement{
				ID:   "req-use-001",
				Kind: specverify.RequirementUse,
				Text: "needs: coverage",
				Source: specverify.SourceSpan{
					File:      skillPath,
					StartLine: 7,
				},
			},
			CoveredBy: []specverify.CoveredBy{},
		}},
	}

	var out bytes.Buffer
	renderSpecVerifyGitHubActions(&out, report, true, false)

	text := out.String()
	assert.Contains(t, text, "file=skills/example/SKILL.md")
	assert.Contains(t, text, "::req-use-001 needs: coverage")
	assert.NotContains(t, text, "needs%3A coverage")
}

func TestSpecVerifyCommandHumanOutputMatchesDocs(t *testing.T) {
	report := &specverify.Report{
		Summary: specverify.Summary{
			TotalRequirements:     1,
			UncoveredRequirements: 1,
		},
		Coverage: []specverify.RequirementCoverage{{
			Requirement: specverify.Requirement{
				ID:   "req-use-001",
				Kind: specverify.RequirementUse,
				Text: "summarize PR discussion",
				Source: specverify.SourceSpan{
					File:      "SKILL.md",
					StartLine: 4,
				},
			},
			CoveredBy: []specverify.CoveredBy{},
		}},
	}

	var out bytes.Buffer
	renderSpecVerifyHuman(&out, report)

	text := out.String()
	assert.Contains(t, text, `MISS req-use-001  "summarize PR discussion"  -> no task exercises this`)
	assert.False(t, strings.ContainsAny(text, "✓✗→"))
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
