package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/require"
)

// pairwiseVerdictEngine answers pairwise judge sessions with a fixed semantic
// winner and delegates actor sessions to the mock engine.
type pairwiseVerdictEngine struct {
	*execution.MockEngine

	mu              sync.Mutex
	winner          string // "skill", "baseline", or "tie"
	positionBiased  bool
	omitVerdict     bool
	failBaseline    bool
	judges          int
	actors          int
	noSkill         int
	shutdowns       int
	usageOnShutdown *models.UsageStats
}

func (e *pairwiseVerdictEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.mu.Lock()
	if !req.EphemeralSession {
		e.actors++
		if req.NoSkills {
			e.noSkill++
		}
		failBaseline := req.NoSkills && e.failBaseline
		e.mu.Unlock()
		if failBaseline {
			return &execution.ExecutionResponse{
				Success: false, ErrorMsg: "baseline unavailable", SessionID: "baseline-failure",
				UsageIsCumulative: true, UsageRevision: 1,
			}, nil
		}
		return e.MockEngine.Execute(ctx, req)
	}
	e.judges++
	// Pass 1 presents A=baseline, B=skill; pass 2 swaps the positions.
	pass1 := e.judges%2 == 1
	e.mu.Unlock()

	if e.omitVerdict {
		return &execution.ExecutionResponse{Success: true, ModelID: "mock", SessionID: "judge"}, nil
	}

	label := "tie"
	if e.positionBiased {
		label = "A"
	} else {
		switch e.winner {
		case "skill":
			label = map[bool]string{true: "B", false: "A"}[pass1]
		case "baseline":
			label = map[bool]string{true: "A", false: "B"}[pass1]
		}
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

func (e *pairwiseVerdictEngine) Shutdown(ctx context.Context) error {
	err := e.MockEngine.Shutdown(ctx)
	e.mu.Lock()
	e.shutdowns++
	e.mu.Unlock()
	return err
}

func (e *pairwiseVerdictEngine) SessionUsage(sessionID string) *models.UsageStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	if sessionID == "baseline-failure" {
		if e.shutdowns > 0 {
			return e.usageOnShutdown
		}
		return &models.UsageStats{}
	}
	return nil
}

func writePairwiseCLISpec(t *testing.T, dir string) string {
	t.Helper()
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
  max_attempts: 1
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
	return spec
}

func TestRunCommand_PairwiseBaselineExitFollowsRelativeJudgments(t *testing.T) {
	for _, tc := range []struct {
		winner               string
		positionBiased       bool
		wantFailure          bool
		wantOperationalError bool
		want                 models.PairwiseSummary
		omitVerdict          bool
	}{
		{winner: "skill", want: models.PairwiseSummary{SkillWins: 2, Judgments: 2, Preference: 1}},
		{winner: "tie", wantFailure: true, want: models.PairwiseSummary{Ties: 2, Judgments: 2}},
		{winner: "baseline", wantFailure: true, want: models.PairwiseSummary{BaselineWins: 2, Judgments: 2, Preference: -1}},
		{winner: "position-biased", positionBiased: true, wantFailure: true, want: models.PairwiseSummary{Inconsistent: 2}},
		{winner: "no-verdict", omitVerdict: true, wantOperationalError: true},
	} {
		t.Run(tc.winner, func(t *testing.T) {
			resetRunGlobals()
			oldFactory := newRunEngine
			t.Cleanup(func() {
				newRunEngine = oldFactory
				resetRunGlobals()
			})
			engine := &pairwiseVerdictEngine{
				MockEngine:     execution.NewMockEngine("mock"),
				winner:         tc.winner,
				positionBiased: tc.positionBiased,
				omitVerdict:    tc.omitVerdict,
			}
			newRunEngine = func(models.Config) (execution.AgentEngine, error) { return engine, nil }

			dir := t.TempDir()
			t.Chdir(dir)
			spec := writePairwiseCLISpec(t, dir)

			output := filepath.Join(dir, "results.json")
			command := newRunCommand()
			command.SetArgs([]string{spec, "--baseline", "--output", output})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			err := command.Execute()
			if tc.wantOperationalError {
				var operational *BenchmarkError
				require.ErrorAs(t, err, &operational)
				var testFailure *TestFailureError
				require.NotErrorAs(t, err, &testFailure)
			} else if tc.wantFailure {
				var failure *TestFailureError
				require.ErrorAs(t, err, &failure)
				require.ErrorContains(t, err, "pairwise judgments show negative/neutral impact")
				if tc.positionBiased {
					require.ErrorContains(t, err, "2 position-inconsistent")
				}
				var operational *BenchmarkError
				require.NotErrorAs(t, err, &operational)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, 4, engine.actors)
			require.Equal(t, 2, engine.noSkill)
			if tc.wantOperationalError {
				require.Equal(t, 2, engine.judges)
			} else {
				require.Equal(t, 4, engine.judges)
			}
			saved, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.True(t, saved.IsBaseline)
			require.NotNil(t, saved.BaselineOutcome)
			if tc.wantOperationalError {
				require.Greater(t, saved.Digest.Errors, 0)
				require.Len(t, saved.TestOutcomes, 1)
				require.Equal(t, models.StatusError, saved.TestOutcomes[0].Status)
				require.Len(t, saved.TestOutcomes[0].Runs, 2)
				for _, run := range saved.TestOutcomes[0].Runs {
					require.Equal(t, models.StatusError, run.Status)
					require.Contains(t, run.ErrorMsg, "judge session completed without calling set_pairwise_winner")
					require.Contains(t, run.FinalOutput, "Mock response for:")
					require.Len(t, run.GraderSessions, 1)
					require.Equal(t, "judge", run.GraderSessions[0].SessionID)
				}
				require.NotNil(t, saved.BaselineOutcome)
				require.NotEmpty(t, saved.BaselineOutcome.TestOutcomes[0].Runs[0].FinalOutput)
				return
			}
			require.Zero(t, saved.Digest.Errors)
			if tc.winner == "skill" {
				for _, run := range saved.TestOutcomes[0].Runs {
					pass1, ok := run.Validations["quality"].Details["pass1"].(map[string]any)
					require.True(t, ok)
					require.Equal(t, "B", pass1["winner"])
					require.Equal(t, "much-better", pass1["magnitude"])
					require.Equal(t, "fixed verdict", pass1["reasoning"])
					pass2, ok := run.Validations["quality"].Details["pass2"].(map[string]any)
					require.True(t, ok)
					require.Equal(t, "A", pass2["winner"])
					require.Equal(t, "much-better", pass2["magnitude"])
					require.Equal(t, "fixed verdict", pass2["reasoning"])
				}
			}
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

func TestRunCommand_PairwiseBaselineFailureSavesOutcome(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	credits := 2.5
	engine := &pairwiseVerdictEngine{
		MockEngine:      execution.NewMockEngine("mock"),
		failBaseline:    true,
		usageOnShutdown: &models.UsageStats{InputTokens: 23, OutputTokens: 5, AICredits: &credits},
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) { return engine, nil }

	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	output := filepath.Join(dir, "results.json")
	command := newRunCommand()
	command.SetArgs([]string{spec, "--baseline", "--output", output})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)

	err := command.Execute()
	require.ErrorContains(t, err, "final baseline attempt")
	var baselineErr *orchestration.BaselineExecutionError
	require.ErrorAs(t, err, &baselineErr)
	require.Equal(t, 1, engine.shutdowns)
	saved, err := models.LoadEvaluationOutcome(output)
	require.NoError(t, err)
	require.True(t, saved.IsBaseline)
	require.Equal(t, true, saved.Metadata["incomplete"])
	require.Greater(t, saved.Digest.Errors, 0)
	require.Len(t, saved.TestOutcomes, 1)
	require.Equal(t, models.StatusError, saved.TestOutcomes[0].Status)
	require.Len(t, saved.TestOutcomes[0].Runs, 2)
	for _, run := range saved.TestOutcomes[0].Runs {
		require.Equal(t, models.StatusError, run.Status)
		require.Equal(t, "baseline unavailable", run.ErrorMsg)
		require.Equal(t, "baseline-failure", run.SessionDigest.SessionID)
		require.NotNil(t, run.SessionDigest.Usage)
		require.Equal(t, 23, run.SessionDigest.Usage.InputTokens)
		require.Equal(t, 5, run.SessionDigest.Usage.OutputTokens)
		require.Equal(t, 2.5, *run.SessionDigest.Usage.AICredits)
	}
	require.Zero(t, engine.judges)
	require.Equal(t, 2, engine.noSkill)
}

func TestRunCommand_PairwiseBaselineFailureOutputDirContinuesModels(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	credits := 2.5
	failedEngine := &pairwiseVerdictEngine{
		MockEngine:      execution.NewMockEngine("mock"),
		failBaseline:    true,
		usageOnShutdown: &models.UsageStats{InputTokens: 23, OutputTokens: 5, AICredits: &credits},
	}
	nextEngine := &pairwiseVerdictEngine{
		MockEngine: execution.NewMockEngine("mock"),
		winner:     "skill",
	}
	var constructed []string
	newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
		constructed = append(constructed, cfg.ModelID)
		switch cfg.ModelID {
		case "model-a":
			return failedEngine, nil
		case "model-b":
			return nextEngine, nil
		default:
			return nil, fmt.Errorf("unexpected model %q", cfg.ModelID)
		}
	}

	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	outputDir := filepath.Join(dir, "results")
	command := newRunCommand()
	command.SetArgs([]string{spec, "--baseline", "--model", "model-a", "--model", "model-b", "--output-dir", outputDir})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)

	err := command.Execute()
	var benchmarkErr *BenchmarkError
	require.ErrorAs(t, err, &benchmarkErr)
	require.ErrorContains(t, err, "final baseline attempt")
	require.Equal(t, []string{"model-a", "model-b"}, constructed)
	require.Equal(t, 1, failedEngine.shutdowns)
	require.Equal(t, 2, nextEngine.noSkill)

	runs, err := os.ReadDir(outputDir)
	require.NoError(t, err)
	var runDir string
	for _, entry := range runs {
		if entry.Name() != ".partial" {
			runDir = entry.Name()
		}
	}
	require.NotEmpty(t, runDir)
	results, err := os.ReadDir(filepath.Join(outputDir, runDir))
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, []string{"model-a.json", "model-b.json"}, []string{results[0].Name(), results[1].Name()})

	failedOutcome, err := models.LoadEvaluationOutcome(filepath.Join(outputDir, runDir, "model-a.json"))
	require.NoError(t, err)
	require.True(t, failedOutcome.IsBaseline)
	require.Equal(t, true, failedOutcome.Metadata["incomplete"])
	require.NotNil(t, failedOutcome.TestOutcomes[0].Runs[0].SessionDigest.Usage)
	require.Equal(t, 23, failedOutcome.TestOutcomes[0].Runs[0].SessionDigest.Usage.InputTokens)

	nextOutcome, err := models.LoadEvaluationOutcome(filepath.Join(outputDir, runDir, "model-b.json"))
	require.NoError(t, err)
	require.True(t, nextOutcome.IsBaseline)
	require.Zero(t, nextOutcome.Digest.Errors)
}

func TestRunCommand_PairwiseBaselineOutputPublicationFailureIsNotBenchmarkError(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	engine := &pairwiseVerdictEngine{MockEngine: execution.NewMockEngine("mock"), failBaseline: true}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) { return engine, nil }

	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	output := filepath.Join(dir, "results.json")
	require.NoError(t, os.Mkdir(output, 0o700))
	command := newRunCommand()
	command.SetArgs([]string{spec, "--baseline", "--output", output})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)

	err := command.Execute()
	require.ErrorContains(t, err, "failed to save output")
	var benchmarkErr *BenchmarkError
	require.NotErrorAs(t, err, &benchmarkErr)
}

func TestRunCommand_PairwiseAdmissionPrecedesEngineConstruction(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	constructions := 0
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		constructions++
		return execution.NewMockEngine("mock"), nil
	}

	command := newRunCommand()
	command.SetArgs([]string{spec})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	require.ErrorContains(t, command.Execute(), "pairwise prompt grading requires --baseline")
	require.Zero(t, constructions)
}

func TestRunCommand_PairwiseRejectsFailFastBeforeEngineConstruction(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task2.yaml"), []byte(`id: pairwise-task-2
name: Pairwise task 2
inputs:
  prompt: compare these outputs
`), 0o600))
	specData, err := os.ReadFile(spec)
	require.NoError(t, err)
	specContents := strings.Replace(string(specData), "  skill_directories: [skills]\n", "  skill_directories: [skills]\n  fail_fast: true\n", 1)
	specContents = strings.Replace(specContents, "  - task.yaml\n", "  - task.yaml\n  - task2.yaml\n", 1)
	require.NoError(t, os.WriteFile(spec, []byte(specContents), 0o600))

	constructions := 0
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		constructions++
		return execution.NewMockEngine("mock"), nil
	}

	command := newRunCommand()
	command.SetArgs([]string{spec, "--baseline"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	require.ErrorContains(t, command.Execute(), "pairwise prompt grading is not supported with fail_fast")
	require.Zero(t, constructions)
}

func TestRunCommand_PairwiseAdmissionIncludesImplicitAgentGrader(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "pairwise-skill.agent.md"), []byte(`---
name: pairwise-skill
description: Pairwise agent fixture.
tools:
  - bash
---
Use the declared tool.
`), 0o600))
	specData, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(spec, []byte(strings.Replace(string(specData), "name: quality", "name: agent_tools_implicit", 1)), 0o600))

	constructions := 0
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		constructions++
		return execution.NewMockEngine("mock"), nil
	}

	command := newRunCommand()
	command.SetArgs([]string{spec, "--baseline"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	require.ErrorContains(t, command.Execute(), `pairwise grader name collision "agent_tools_implicit"`)
	require.Zero(t, constructions)
}

func TestRunCommand_SkipGradersBypassesPairwiseAdmission(t *testing.T) {
	resetRunGlobals()
	oldFactory := newRunEngine
	t.Cleanup(func() {
		newRunEngine = oldFactory
		resetRunGlobals()
	})
	dir := t.TempDir()
	t.Chdir(dir)
	spec := writePairwiseCLISpec(t, dir)
	constructions := 0
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		constructions++
		return execution.NewMockEngine("mock"), nil
	}

	command := newRunCommand()
	command.SetArgs([]string{spec, "--skip-graders"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	require.NoError(t, command.Execute())
	require.Equal(t, 1, constructions)
}
