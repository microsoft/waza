package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvalSpecReasoningEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reasoning.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`name: reasoning
skill: test-skill
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: copilot-sdk
  model: gpt-5
  reasoning_effort: high
  judge_reasoning_effort: low
graders:
  - type: prompt
    name: judge
    config:
      reasoning_effort: medium
      prompt: grade the response
`), 0o644))

	spec, err := LoadEvalSpec(path)
	require.NoError(t, err)
	require.Equal(t, "high", spec.Config.ReasoningEffort)
	require.Equal(t, "low", spec.Config.JudgeReasoningEffort)
	require.Equal(t, "medium", spec.Graders[0].Parameters.(PromptGraderParameters).ReasoningEffort)
}

func TestEvalSpecRejectsInvalidReasoningEffort(t *testing.T) {
	spec := &EvalSpec{Config: Config{TrialsPerTask: 1, TimeoutSec: 60, EngineType: "copilot-sdk", ReasoningEffort: "extra-high"}}
	require.ErrorContains(t, spec.Validate(), "reasoning_effort must be one of")

	spec.Config.ReasoningEffort = "high"
	spec.Config.EngineType = "mock"
	require.ErrorContains(t, spec.Validate(), "require executor copilot-sdk")
}
