package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type suggestTestEngine struct {
	outputs    []string // sequential outputs for each Execute call
	err        error
	callIdx    int
	initCalled bool
}

func (e *suggestTestEngine) Initialize(context.Context) error {
	e.initCalled = true
	return nil
}

func (e *suggestTestEngine) Execute(_ context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if !e.initCalled {
		return nil, fmt.Errorf("initialize was not called before Execute!")
	}

	if e.err != nil {
		return nil, e.err
	}
	idx := e.callIdx
	e.callIdx++
	if idx < len(e.outputs) {
		return &execution.ExecutionResponse{FinalOutput: e.outputs[idx], Success: true}, nil
	}
	// fallback to last output if more calls than outputs
	return &execution.ExecutionResponse{FinalOutput: e.outputs[len(e.outputs)-1], Success: true}, nil
}

func (e *suggestTestEngine) Shutdown(context.Context) error { return nil }

func (e *suggestTestEngine) SessionUsage(string) *models.UsageStats { return nil }

func writeSuggestSkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	skillContent := `---
name: suggest-skill
description: "Test skill. USE FOR: summarize, explain. DO NOT USE FOR: deployment."
---

# Suggest Skill

## Overview
Helpful content.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0o644))
	return dir
}

func TestSuggestCommand_DryRunYAML(t *testing.T) {
	skillDir := writeSuggestSkill(t)
	engineOutput := `eval_yaml: |
  name: generated-eval
  description: generated
  skill: suggest-skill
  version: "1.0"
  config:
    trials_per_task: 1
    timeout_seconds: 120
    parallel: false
    executor: mock
    model: test
  graders:
    - type: code
      name: has_output
      config:
        assertions:
          - "len(output) > 0"
  metrics:
    - name: completion
      weight: 1.0
      threshold: 0.8
  tasks:
    - "tasks/*.yaml"
tasks:
  - path: tasks/basic.yaml
    content: |
      id: basic-001
      name: Basic
      inputs:
        prompt: "hello"
`

	selectionOutput := "graders:\n  - code\n"

	orig := newSuggestEngine
	newSuggestEngine = func(string) execution.AgentEngine {
		return &suggestTestEngine{outputs: []string{selectionOutput, engineOutput}}
	}
	t.Cleanup(func() { newSuggestEngine = orig })

	cmd := newSuggestCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "eval_yaml:")
	require.Contains(t, out.String(), "tasks:")
	require.NoFileExists(t, filepath.Join(skillDir, "evals", "eval.yaml"))
}

func TestSuggestCommand_ApplyWritesFiles(t *testing.T) {
	skillDir := writeSuggestSkill(t)
	engineOutput := `eval_yaml: |
  name: generated-eval
  description: generated
  skill: suggest-skill
  version: "1.0"
  config:
    trials_per_task: 1
    timeout_seconds: 120
    parallel: false
    executor: mock
    model: test
  graders:
    - type: code
      name: has_output
      config:
        assertions:
          - "len(output) > 0"
  metrics:
    - name: completion
      weight: 1.0
      threshold: 0.8
  tasks:
    - "tasks/*.yaml"
tasks:
  - path: tasks/basic.yaml
    content: |
      id: basic-001
      name: Basic
      inputs:
        prompt: "hello"
fixtures:
  - path: fixtures/sample.txt
    content: |
      fixture
`

	selectionOutput := "graders:\n  - code\n"

	orig := newSuggestEngine
	newSuggestEngine = func(string) execution.AgentEngine {
		return &suggestTestEngine{outputs: []string{selectionOutput, engineOutput}}
	}
	t.Cleanup(func() { newSuggestEngine = orig })

	cmd := newSuggestCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir, "--apply"})

	require.NoError(t, cmd.Execute())
	require.FileExists(t, filepath.Join(skillDir, "evals", "eval.yaml"))
	require.FileExists(t, filepath.Join(skillDir, "evals", "tasks", "basic.yaml"))
	require.FileExists(t, filepath.Join(skillDir, "evals", "fixtures", "sample.txt"))
	require.Contains(t, out.String(), "output_dir")
}

func TestSuggestCommand_InvalidResponseFromMockEngine(t *testing.T) {
	skillDir := writeSuggestSkill(t)

	orig := newSuggestEngine
	newSuggestEngine = func(model string) execution.AgentEngine { return execution.NewMockEngine(model) }
	t.Cleanup(func() { newSuggestEngine = orig })

	cmd := newSuggestCommand()
	cmd.SetArgs([]string{skillDir})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing suggest response")
}

func TestSuggestCommand_MissingSkill(t *testing.T) {
	orig := newSuggestEngine
	newSuggestEngine = func(model string) execution.AgentEngine { return execution.NewMockEngine(model) }
	t.Cleanup(func() { newSuggestEngine = orig })

	cmd := newSuggestCommand()
	cmd.SetArgs([]string{"/tmp/does-not-exist"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "skill path does not exist")
}
