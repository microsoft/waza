package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegradeOutcome_ClearsOmittedJudgeEffort(t *testing.T) {
	original := &models.EvaluationOutcome{
		Setup: models.OutcomeSetup{RunsPerTest: 1, ReasoningEffort: "high", JudgeReasoningEffort: "max"},
	}
	result := RegradeOutcome(original, nil, "", "")
	assert.Empty(t, result.Setup.JudgeReasoningEffort)
	assert.Equal(t, "high", result.Setup.ReasoningEffort)
	assert.Equal(t, "max", original.Setup.JudgeReasoningEffort)
	data, err := json.Marshal(result.Setup)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "judge_reasoning_effort")
	assert.Contains(t, string(data), `"reasoning_effort":"high"`)
}

func TestComputeTestStats_Nil(t *testing.T) {
	assert.Nil(t, ComputeTestStats(nil))
}

func TestDigestHelpers_Nil(t *testing.T) {
	assert.Equal(t, 0.0, computeAggregateScore(nil))
	assert.Equal(t, 0.0, computeWeightedAggregateScore(nil))

	minScore, maxScore, stdDev := computeDigestScoreStats(nil)
	assert.Equal(t, 0.0, minScore)
	assert.Equal(t, 0.0, maxScore)
	assert.Equal(t, 0.0, stdDev)
}

func TestBuildDigest_SinglePassedTask(t *testing.T) {
	outcomes := []models.TestOutcome{{
		Status: models.StatusPassed,
		Stats:  &models.TestStats{AvgScore: 1.0, AvgWeightedScore: 1.0, PassRate: 1.0},
	}}
	d := BuildDigest(outcomes, 500, 1)
	assert.Equal(t, 1, d.TotalTests)
	assert.Equal(t, 1, d.Succeeded)
	assert.InDelta(t, 1.0, d.SuccessRate, 0.001)
	assert.InDelta(t, 1.0, d.AggregateScore, 0.001)
}

func TestBuildDigest_MixedTasks(t *testing.T) {
	outcomes := []models.TestOutcome{
		{Status: models.StatusPassed, Stats: &models.TestStats{AvgScore: 1.0, AvgWeightedScore: 1.0}},
		{Status: models.StatusFailed, Stats: &models.TestStats{AvgScore: 0.0, AvgWeightedScore: 0.0}},
	}
	d := BuildDigest(outcomes, 1000, 1)
	assert.Equal(t, 2, d.TotalTests)
	assert.Equal(t, 1, d.Succeeded)
	assert.Equal(t, 1, d.Failed)
	assert.InDelta(t, 0.5, d.SuccessRate, 0.001)
	assert.InDelta(t, 0.5, d.AggregateScore, 0.001)
}

func trialRuns(passed, total int) []models.RunResult {
	runs := make([]models.RunResult, total)
	for i := range runs {
		runs[i].Status = models.StatusFailed
		if i < passed {
			runs[i].Status = models.StatusPassed
		}
	}
	return runs
}

func TestComputeTestStats_PassRateCI(t *testing.T) {
	stats := ComputeTestStats(trialRuns(2, 3))
	require.NotNil(t, stats.PassRateCI)
	assert.InDelta(t, 2.0/3.0, stats.PassRateCI.Mean, 1e-9)
	assert.InDelta(t, 0.2077, stats.PassRateCI.Lower, 1e-4)
	assert.InDelta(t, 0.9385, stats.PassRateCI.Upper, 1e-4)

	assert.Nil(t, ComputeTestStats(trialRuns(1, 1)).PassRateCI, "a single trial has no interval")
}

func TestComputeTestStats_NoSignificanceOnAbsoluteScores(t *testing.T) {
	// A CI on a single task's own score says nothing about significance, so it must not be reported.
	data, err := json.Marshal(ComputeTestStats(trialRuns(3, 3)))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "is_significant")
}

func TestBuildDigest_ReliabilityStatistics(t *testing.T) {
	outcomes := []models.TestOutcome{
		{Status: models.StatusPassed, Stats: ComputeTestStats(trialRuns(3, 3))},
		{Status: models.StatusFailed, Stats: ComputeTestStats(trialRuns(1, 3))},
	}
	d := BuildDigest(outcomes, 1000, 3)
	require.NotNil(t, d.Statistics)

	// pass^k averages C(c,k)/C(n,k) over tasks: (1 + 1/3)/2, (1 + 0)/2, (1 + 0)/2.
	require.Len(t, d.Statistics.PassHatK, 3)
	assert.InDelta(t, 2.0/3.0, d.Statistics.PassHatK[0], 1e-9)
	assert.InDelta(t, 0.5, d.Statistics.PassHatK[1], 1e-9)
	assert.InDelta(t, 0.5, d.Statistics.PassHatK[2], 1e-9)

	// Success rate interval treats each task as one sample: 1 of 2 tasks passed.
	require.NotNil(t, d.Statistics.SuccessRateCI)
	assert.InDelta(t, 0.5, d.Statistics.SuccessRateCI.Mean, 1e-9)
	assert.InDelta(t, 0.0945, d.Statistics.SuccessRateCI.Lower, 1e-4)
	assert.InDelta(t, 0.9055, d.Statistics.SuccessRateCI.Upper, 1e-4)

	data, err := json.Marshal(d.Statistics)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "is_significant")
}

func TestBuildDigest_PassHatKStopsAtFewestTrials(t *testing.T) {
	outcomes := []models.TestOutcome{
		{Status: models.StatusPassed, Stats: ComputeTestStats(trialRuns(3, 3))},
		{Status: models.StatusPassed, Stats: ComputeTestStats(trialRuns(2, 2))},
	}
	d := BuildDigest(outcomes, 1000, 3)
	require.NotNil(t, d.Statistics)
	assert.Equal(t, []float64{1, 1}, d.Statistics.PassHatK)
}

func TestRegradeOutcome_ComputesStatsAndDigest(t *testing.T) {
	original := &models.EvaluationOutcome{
		RunID:       "run-1",
		SkillTested: "test-skill",
		BenchName:   "test-bench",
		Setup:       models.OutcomeSetup{RunsPerTest: 1, ModelID: "m"},
		Digest:      models.OutcomeDigest{DurationMs: 1000},
	}

	gradedOutcomes := []models.TestOutcome{{
		TestID: "t1",
		Status: models.StatusPassed,
		Runs: []models.RunResult{{
			Status:      models.StatusPassed,
			Validations: map[string]models.GraderResults{"g": {Score: 1.0, Passed: true, Weight: 1.0}},
		}},
	}}

	result := RegradeOutcome(original, gradedOutcomes, "judge-model", "high")

	require.NotNil(t, result.TestOutcomes[0].Stats)
	assert.InDelta(t, 1.0, result.TestOutcomes[0].Stats.PassRate, 0.001)
	assert.Equal(t, 1, result.Digest.Succeeded)
	assert.InDelta(t, 1.0, result.Digest.SuccessRate, 0.001)
	assert.Equal(t, "judge-model", result.Setup.JudgeModel)
	assert.Equal(t, "high", result.Setup.JudgeReasoningEffort)
}
