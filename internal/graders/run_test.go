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
