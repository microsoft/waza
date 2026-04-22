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

type qualityTestEngine struct {
	output     string
	err        error
	initCalled bool
	initErr    error
}

func (e *qualityTestEngine) Initialize(context.Context) error {
	e.initCalled = true
	return e.initErr
}

func (e *qualityTestEngine) Execute(_ context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if !e.initCalled {
		return nil, fmt.Errorf("initialize was not called before Execute!")
	}
	if e.err != nil {
		return nil, e.err
	}
	return &execution.ExecutionResponse{FinalOutput: e.output, Success: true}, nil
}

func (e *qualityTestEngine) Shutdown(context.Context) error         { return nil }
func (e *qualityTestEngine) SessionUsage(string) *models.UsageStats { return nil }

func writeQualitySkill(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := `---
name: quality-test-skill
description: "A test skill. USE FOR: testing, debugging. DO NOT USE FOR: production deploys."
---

# Quality Test Skill

## Overview
This skill helps with testing and debugging workflows.

## Steps
1. Analyze the test input
2. Generate appropriate test output
3. Report results

## Error Handling
If input is invalid, return an error message.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	return dir
}

const qualityValidJSON = `{
  "dimensions": [
    {"name": "clarity", "score": 4, "feedback": "Instructions are clear and well-structured."},
    {"name": "completeness", "score": 3, "feedback": "Missing some edge case documentation."},
    {"name": "trigger_precision", "score": 5, "feedback": "USE FOR and DO NOT USE FOR are precise and non-overlapping."},
    {"name": "scope_coverage", "score": 4, "feedback": "Scope is well-defined with clear boundaries."},
    {"name": "anti_patterns", "score": 3, "feedback": "Some steps could be more specific to avoid ambiguity."}
  ],
  "overall_score": 3.8,
  "summary": "A solid skill with good clarity and triggers. Completeness and anti-pattern avoidance could be improved."
}`

func TestQualityCommand_TableOutput(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir})

	require.NoError(t, cmd.Execute())
	output := out.String()
	require.Contains(t, output, "clarity")
	require.Contains(t, output, "completeness")
	require.Contains(t, output, "trigger_precision")
	require.Contains(t, output, "scope_coverage")
	require.Contains(t, output, "anti_patterns")
	require.Contains(t, output, "Overall: 3.8/5.0")
}

func TestQualityCommand_JSONOutput(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir, "--format", "json"})

	require.NoError(t, cmd.Execute())
	output := out.String()
	require.Contains(t, output, `"overall_score"`)
	require.Contains(t, output, `"dimensions"`)
	require.Contains(t, output, `"clarity"`)
}

func TestQualityCommand_InvalidFormat(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	cmd.SetArgs([]string{skillDir, "--format", "xml"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid format")
}

func TestQualityCommand_AuthError(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{
			initErr: fmt.Errorf("failed to get copilot authentication status"),
		}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "copilot login")
}

func TestQualityCommand_EngineError(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{err: fmt.Errorf("LLM unavailable")}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "judge execution")
}

func TestQualityCommand_MissingSkill(t *testing.T) {
	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	cmd.SetArgs([]string{"/nonexistent/skill/path"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "skill path does not exist")
}

func TestQualityCommand_CustomRubricNotYetSupported(t *testing.T) {
	skillDir := writeQualitySkill(t)

	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	cmd.SetArgs([]string{skillDir, "--rubric", "custom.yaml"})

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not yet supported")
}

func TestQualityCommand_NoArgs(t *testing.T) {
	orig := newQualityEngine
	newQualityEngine = func(string) execution.AgentEngine {
		return &qualityTestEngine{output: qualityValidJSON}
	}
	t.Cleanup(func() { newQualityEngine = orig })

	cmd := newQualityCommand()
	err := cmd.Execute()
	require.Error(t, err) // cobra requires exactly 1 arg
}
