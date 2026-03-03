package baseline

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestComputeImprovement_SkillBetter(t *testing.T) {
	baseline := &models.RunResult{
		Status:     models.StatusFailed,
		DurationMs: 10000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.3, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 10, InputTokens: 5000},
		},
	}
	withSkill := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 7000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.9, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 6, InputTokens: 3000},
		},
	}

	improvement, breakdown := ComputeImprovement(baseline, withSkill)

	assert.Greater(t, improvement, 0.0, "skill should show improvement")
	assert.InDelta(t, 0.6, breakdown.QualityDelta, 0.01)
	assert.InDelta(t, -0.4, breakdown.TokenReduction, 0.01)
	assert.InDelta(t, -0.4, breakdown.TurnReduction, 0.01)
	assert.InDelta(t, -0.3, breakdown.TimeReduction, 0.01)
	assert.InDelta(t, 1.0, breakdown.TaskCompletion, 0.01)
}

func TestComputeImprovement_SkillWorse(t *testing.T) {
	baseline := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 5000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.9, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 5, InputTokens: 2000},
		},
	}
	withSkill := &models.RunResult{
		Status:     models.StatusFailed,
		DurationMs: 12000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.2, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 15, InputTokens: 8000},
		},
	}

	improvement, breakdown := ComputeImprovement(baseline, withSkill)

	assert.Less(t, improvement, 0.0, "skill should show regression")
	assert.InDelta(t, -0.7, breakdown.QualityDelta, 0.01)
	assert.InDelta(t, 3.0, breakdown.TokenReduction, 0.01, "more tokens = positive reduction")
	assert.InDelta(t, 2.0, breakdown.TurnReduction, 0.01)
	assert.InDelta(t, 1.4, breakdown.TimeReduction, 0.01)
	assert.InDelta(t, -1.0, breakdown.TaskCompletion, 0.01)
}

func TestComputeImprovement_Equal(t *testing.T) {
	run := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 5000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.8, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 5, InputTokens: 2000},
		},
	}

	improvement, breakdown := ComputeImprovement(run, run)

	assert.InDelta(t, 0.0, improvement, 0.001)
	assert.InDelta(t, 0.0, breakdown.QualityDelta, 0.001)
	assert.InDelta(t, 0.0, breakdown.TokenReduction, 0.001)
	assert.InDelta(t, 0.0, breakdown.TurnReduction, 0.001)
	assert.InDelta(t, 0.0, breakdown.TimeReduction, 0.001)
	assert.InDelta(t, 0.0, breakdown.TaskCompletion, 0.001)
}

func TestComputeImprovement_ZeroBaseline(t *testing.T) {
	baseline := &models.RunResult{
		Status:     models.StatusFailed,
		DurationMs: 0,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.0, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 0, InputTokens: 0},
		},
	}
	withSkill := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 5000,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 1.0, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 5, InputTokens: 3000},
		},
	}

	improvement, breakdown := ComputeImprovement(baseline, withSkill)

	assert.Greater(t, improvement, 0.0)
	assert.InDelta(t, 1.0, breakdown.QualityDelta, 0.01)
	// Zero baseline tokens/turns/time → no reduction computed
	assert.InDelta(t, 0.0, breakdown.TokenReduction, 0.01)
	assert.InDelta(t, 0.0, breakdown.TurnReduction, 0.01)
	assert.InDelta(t, 0.0, breakdown.TimeReduction, 0.01)
	assert.InDelta(t, 1.0, breakdown.TaskCompletion, 0.01)
}

func TestComputeImprovement_ClampedToRange(t *testing.T) {
	// Even extreme values should be clamped to [-1, 1]
	baseline := &models.RunResult{
		Status:     models.StatusFailed,
		DurationMs: 100,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 0.0, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 1, InputTokens: 100},
		},
	}
	withSkill := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 1,
		Validations: map[string]models.GraderResults{
			"g1": {Score: 1.0, Weight: 1.0},
		},
		SessionDigest: models.SessionDigest{
			Usage: &models.UsageStats{Turns: 1, InputTokens: 10},
		},
	}

	improvement, _ := ComputeImprovement(baseline, withSkill)
	assert.LessOrEqual(t, improvement, 1.0)
	assert.GreaterOrEqual(t, improvement, -1.0)
}

func TestComputeFromOutcomes_EmptyRuns(t *testing.T) {
	withSkill := &models.TestOutcome{
		DisplayName: "test-task",
		Runs:        []models.RunResult{},
	}
	baseline := &models.TestOutcome{
		DisplayName: "test-task",
		Runs:        []models.RunResult{},
	}

	result := ComputeFromOutcomes(withSkill, baseline)
	assert.Equal(t, "test-task", result.TaskName)
	assert.InDelta(t, 0.0, result.Improvement, 0.001)
}

func TestComputeFromOutcomes_WithRuns(t *testing.T) {
	withSkill := &models.TestOutcome{
		DisplayName: "test-task",
		Runs: []models.RunResult{
			{
				Status:     models.StatusPassed,
				DurationMs: 5000,
				Validations: map[string]models.GraderResults{
					"g1": {Score: 0.9, Weight: 1.0},
				},
				SessionDigest: models.SessionDigest{
					Usage: &models.UsageStats{Turns: 5, InputTokens: 2000},
				},
			},
		},
	}
	baseline := &models.TestOutcome{
		DisplayName: "test-task",
		Runs: []models.RunResult{
			{
				Status:     models.StatusFailed,
				DurationMs: 8000,
				Validations: map[string]models.GraderResults{
					"g1": {Score: 0.3, Weight: 1.0},
				},
				SessionDigest: models.SessionDigest{
					Usage: &models.UsageStats{Turns: 10, InputTokens: 5000},
				},
			},
		},
	}

	result := ComputeFromOutcomes(withSkill, baseline)
	assert.Equal(t, "test-task", result.TaskName)
	assert.Greater(t, result.Improvement, 0.0)
	assert.NotNil(t, result.WithSkill)
	assert.NotNil(t, result.Baseline)
}
