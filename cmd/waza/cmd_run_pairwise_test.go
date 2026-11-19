package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// pairwiseVerdictEngine answers pairwise judge sessions with a fixed semantic
// winner and delegates actor sessions to the mock engine.
type pairwiseVerdictEngine struct {
	*execution.MockEngine

	mu      sync.Mutex
	winner  string // "skill", "baseline", or "tie"
	judges  int
	actors  int
	noSkill int
}

func (e *pairwiseVerdictEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.mu.Lock()
	if !req.EphemeralSession {
		e.actors++
		if req.NoSkills {
			e.noSkill++
		}
		e.mu.Unlock()
		return e.MockEngine.Execute(ctx, req)
	}
	e.judges++
	// Pass 1 presents A=baseline, B=skill; pass 2 swaps the positions.
	pass1 := e.judges%2 == 1
	e.mu.Unlock()

	label := "tie"
	switch e.winner {
	case "skill":
		label = map[bool]string{true: "B", false: "A"}[pass1]
	case "baseline":
		label = map[bool]string{true: "A", false: "B"}[pass1]
	}
	for _, tool := range req.Tools {
		if tool.Name == "set_pairwise_winner" {
			if _, err := tool.Handler(copilot.ToolInvocation{Arguments: map[string]any{
				"winner": label, "magnitude": "much-better", "reasoning": "fixed verdict",
			}}); err != nil {
				return nil, err
			}
		}
	}
	return &execution.ExecutionResponse{Success: true, ModelID: "mock", SessionID: "judge"}, nil
}

func TestRunCommand_PairwiseBaselineExitFollowsRelativeJudgments(t *testing.T) {
	for _, tc := range []struct {
		winner      string
		wantFailure bool
		want        models.PairwiseSummary
	}{
		{winner: "skill", want: models.PairwiseSummary{SkillWins: 2, Judgments: 2, Preference: 1}},
		{winner: "tie", wantFailure: true, want: models.PairwiseSummary{Ties: 2, Judgments: 2}},
		{winner: "baseline", wantFailure: true, want: models.PairwiseSummary{BaselineWins: 2, Judgments: 2, Preference: -1}},
	} {
		t.Run(tc.winner, func(t *testing.T) {
			resetRunGlobals()
			oldFactory := newRunEngine
			t.Cleanup(func() {
				newRunEngine = oldFactory
				resetRunGlobals()
			})
			engine := &pairwiseVerdictEngine{MockEngine: execution.NewMockEngine("mock"), winner: tc.winner}
			newRunEngine = func(models.Config) (execution.AgentEngine, error) { return engine, nil }

			dir := t.TempDir()
			t.Chdir(dir)
			skillDir := filepath.Join(dir, "skills", "pairwise-skill")
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
				[]byte("---\nname: pairwise-skill\ndescription: Pairwise fixture.\n---\nBe better.\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(`id: pairwise-task
name: Pairwise task
inputs:
  prompt: compare these outputs
`), 0o600))
			spec := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(spec, []byte(`name: pairwise-cli
skill: pairwise-skill
version: "1.0"
config:
  trials_per_task: 2
  timeout_seconds: 30
  executor: mock
  model: mock
  judge_model: mock
  skill_directories: [skills]
graders:
  - type: prompt
    name: quality
    config:
      mode: pairwise
      prompt: Choose the better output.
tasks:
  - task.yaml
`), 0o600))

			output := filepath.Join(dir, "results.json")
			command := newRunCommand()
			command.SetArgs([]string{spec, "--baseline", "--output", output})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			err := command.Execute()
			if tc.wantFailure {
				var failure *TestFailureError
				require.ErrorAs(t, err, &failure)
				require.ErrorContains(t, err, "pairwise judgments show negative/neutral impact")
				var operational *BenchmarkError
				require.NotErrorAs(t, err, &operational)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, 4, engine.actors)
			require.Equal(t, 2, engine.noSkill)
			require.Equal(t, 4, engine.judges)
			saved, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.True(t, saved.IsBaseline)
			require.NotNil(t, saved.BaselineOutcome)
			require.Zero(t, saved.Digest.Errors)
			require.Equal(t, 1.0, saved.BaselineOutcome.Digest.SuccessRate,
				"the baseline pass omits its sole pairwise grader, so absolute pass rates cannot express impact")
			require.Len(t, saved.TestOutcomes, 1)
			impact := saved.TestOutcomes[0].SkillImpact
			require.NotNil(t, impact)
			require.NotNil(t, impact.PairwiseSummary)
			require.Equal(t, tc.want, *impact.PairwiseSummary)
		})
	}
}
