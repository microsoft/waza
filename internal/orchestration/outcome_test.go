package orchestration

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	result := RegradeOutcome(original, gradedOutcomes, "judge-model")

	require.NotNil(t, result.TestOutcomes[0].Stats)
	assert.InDelta(t, 1.0, result.TestOutcomes[0].Stats.PassRate, 0.001)
	assert.Equal(t, 1, result.Digest.Succeeded)
	assert.InDelta(t, 1.0, result.Digest.SuccessRate, 0.001)
	assert.Equal(t, "judge-model", result.Setup.JudgeModel)
}
