package graders

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestApplyDefaults_PromptGrader(t *testing.T) {
	t.Run("sets judge model when empty", func(t *testing.T) {
		p := models.PromptGraderParameters{Prompt: "check"}
		result := applyDefaults(p, "gpt-4o", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "gpt-4o", pp.Model)
		assert.Equal(t, "check", pp.Prompt)
	})

	t.Run("preserves existing model", func(t *testing.T) {
		p := models.PromptGraderParameters{Model: "existing"}
		result := applyDefaults(p, "gpt-4o", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "existing", pp.Model)
	})

	t.Run("no judge model", func(t *testing.T) {
		p := models.PromptGraderParameters{Prompt: "check"}
		result := applyDefaults(p, "", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "", pp.Model)
	})
}

func TestApplyDefaults_DiffGrader(t *testing.T) {
	t.Run("sets update snapshots", func(t *testing.T) {
		p := models.DiffGraderParameters{}
		result := applyDefaults(p, "", true)
		dp, ok := result.(models.DiffGraderParameters)
		assert.True(t, ok)
		assert.True(t, dp.UpdateSnapshots)
	})

	t.Run("no update snapshots", func(t *testing.T) {
		p := models.DiffGraderParameters{}
		result := applyDefaults(p, "", false)
		dp, ok := result.(models.DiffGraderParameters)
		assert.True(t, ok)
		assert.False(t, dp.UpdateSnapshots)
	})
}

func TestApplyDefaults_OtherGrader(t *testing.T) {
	p := models.TextGraderParameters{Contains: []string{"hello"}}
	result := applyDefaults(p, "gpt-4o", true)
	tp, ok := result.(models.TextGraderParameters)
	assert.True(t, ok)
	assert.Equal(t, []string{"hello"}, tp.Contains)
}

// --- Expectation evaluation tests ---

func TestEvaluateExpectations_MayInclude(t *testing.T) {
	t.Run("passes when any match found", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MayInclude: []string{"alpha", "beta", "gamma"},
			},
		}
		gCtx := &Context{Output: "The result is Beta plus delta"}
		results := evaluateExpectations(tc, gCtx)
		r, ok := results["_output_contains_any"]
		assert.True(t, ok)
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
		assert.Contains(t, r.Feedback, "beta")
	})

	t.Run("fails when none match", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MayInclude: []string{"alpha", "beta"},
			},
		}
		gCtx := &Context{Output: "nothing relevant here"}
		results := evaluateExpectations(tc, gCtx)
		r, ok := results["_output_contains_any"]
		assert.True(t, ok)
		assert.Equal(t, 0.0, r.Score)
		assert.False(t, r.Passed)
	})

	t.Run("skipped when empty", func(t *testing.T) {
		tc := &models.TestCase{}
		gCtx := &Context{Output: "anything"}
		results := evaluateExpectations(tc, gCtx)
		_, ok := results["_output_contains_any"]
		assert.False(t, ok)
	})
}

func TestEvaluateExpectations_MustInclude(t *testing.T) {
	t.Run("all present", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustInclude: []string{"hello", "world"},
			},
		}
		gCtx := &Context{Output: "Hello World"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_contains"]
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
	})

	t.Run("partial match", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustInclude: []string{"hello", "world"},
			},
		}
		gCtx := &Context{Output: "Hello there"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_contains"]
		assert.Equal(t, 0.5, r.Score)
		assert.False(t, r.Passed)
	})
}

func TestEvaluateExpectations_MustExclude(t *testing.T) {
	t.Run("none present passes", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustExclude: []string{"error", "fail"},
			},
		}
		gCtx := &Context{Output: "all good"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_not_contains"]
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
	})

	t.Run("some present fails", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustExclude: []string{"error", "fail"},
			},
		}
		gCtx := &Context{Output: "error occurred"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_not_contains"]
		assert.Equal(t, 0.5, r.Score)
		assert.False(t, r.Passed)
	})
}

func TestEvaluateExpectations_Combined(t *testing.T) {
	tc := &models.TestCase{
		Expectation: models.TaskExpectation{
			MustInclude: []string{"result"},
			MustExclude: []string{"error"},
			MayInclude:  []string{"option_a", "option_b"},
		},
	}
	gCtx := &Context{Output: "The result is option_b"}
	results := evaluateExpectations(tc, gCtx)
	assert.Len(t, results, 3)
	assert.True(t, results["_output_contains"].Passed)
	assert.True(t, results["_output_not_contains"].Passed)
	assert.True(t, results["_output_contains_any"].Passed)
}
