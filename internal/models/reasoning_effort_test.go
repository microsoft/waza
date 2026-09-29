package models

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskJudgeReasoningEffort(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max", "invalid"} {
			t.Run(fmt.Sprintf("checkpoint=%t/effort=%s", checkpoint, effort), func(t *testing.T) {
				graders := "graders:\n  - type: prompt\n    name: judge\n    config:\n      prompt: grade\n"
				if effort != "" {
					graders += "      reasoning_effort: " + effort + "\n"
				}
				if checkpoint {
					graders = "checkpoints:\n  - after_turn: 1\n    " + strings.ReplaceAll(strings.TrimSpace(graders), "\n", "\n    ") + "\n"
				}
				path := filepath.Join(t.TempDir(), "task.yaml")
				require.NoError(t, os.WriteFile(path, []byte("id: task\ninputs:\n  prompt: hello\n"+graders), 0o600))
				tc, err := LoadTestCase(path)
				if effort == "invalid" {
					require.ErrorContains(t, err, "reasoning_effort must be one of")
					return
				}
				require.NoError(t, err)
				require.NoError(t, tc.ValidateForExecutor("copilot-sdk"))
				for _, executor := range []string{"mock", ""} {
					err := tc.ValidateForExecutor(executor)
					if effort == "" {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "reasoning_effort requires executor copilot-sdk")
					}
				}
			})
		}
	}
}

func TestValidatorInlineReasoningEffortType(t *testing.T) {
	v := ValidatorInline{Kind: GraderKindPrompt, Parameters: TextGraderParameters{}}
	require.ErrorContains(t, v.Validate(), "expected PromptGraderParameters")
}

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
	params, ok := spec.Graders[0].Parameters.(PromptGraderParameters)
	require.True(t, ok)
	require.Equal(t, "medium", params.ReasoningEffort)
}

func TestEvalSpecRejectsInvalidReasoningEffort(t *testing.T) {
	spec := &EvalSpec{Config: Config{TrialsPerTask: 1, TimeoutSec: 60, EngineType: "copilot-sdk", ReasoningEffort: "extra-high"}}
	require.ErrorContains(t, spec.Validate(), "reasoning_effort must be one of")

	spec.Config.ReasoningEffort = "high"
	spec.Config.EngineType = "mock"
	require.ErrorContains(t, spec.Validate(), "require executor copilot-sdk")
}

func TestEvalSpecRejectsGraderReasoningEffortForNonCopilotExecutor(t *testing.T) {
	spec := &EvalSpec{
		Config: Config{TrialsPerTask: 1, TimeoutSec: 60, EngineType: "mock"},
		Graders: []GraderConfig{{
			Kind:       GraderKindPrompt,
			Identifier: "judge",
			Parameters: PromptGraderParameters{Prompt: "grade", ReasoningEffort: "medium"},
		}},
	}
	require.ErrorContains(t, spec.Validate(), "reasoning_effort requires executor copilot-sdk")

	spec.Config.EngineType = "copilot-sdk"
	require.NoError(t, spec.Validate())
}
