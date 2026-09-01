package cache

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheKeyDifferentReasoningEffortChangesKey(t *testing.T) {
	spec := func(effort, judgeEffort string) *models.EvalSpec {
		return &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "test"}, SkillName: "skill", Config: models.Config{
			ModelID: "gpt-5", EngineType: "copilot-sdk", TimeoutSec: 300,
			ReasoningEffort: effort, JudgeReasoningEffort: judgeEffort,
		}}
	}
	task := &models.TestCase{TestID: "test-1", Stimulus: models.TaskStimulus{Message: "Test"}}

	low, err := CacheKey(spec("low", "low"), task, "")
	require.NoError(t, err)
	high, err := CacheKey(spec("high", "low"), task, "")
	require.NoError(t, err)
	assert.NotEqual(t, low, high)

	judgeHigh, err := CacheKey(spec("low", "high"), task, "")
	require.NoError(t, err)
	assert.NotEqual(t, low, judgeHigh)
}
