package graders

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTriggerHeuristicGrader_ConstructorValidation(t *testing.T) {
	skillPath := writeTestSkillFile(t)

	t.Run("missing skill_path returns error", func(t *testing.T) {
		_, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			Mode: "positive",
		})
		require.Error(t, err)
	})

	t.Run("invalid mode returns error", func(t *testing.T) {
		_, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "maybe",
		})
		require.Error(t, err)
	})

	t.Run("invalid threshold returns error", func(t *testing.T) {
		threshold := 1.2
		_, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "positive",
			Threshold: &threshold,
		})
		require.Error(t, err)
	})
}

func TestTriggerHeuristicGrader_PositiveAndNegativeModes(t *testing.T) {
	skillPath := writeTestSkillFile(t)

	t.Run("positive mode passes for matching prompt", func(t *testing.T) {
		g, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "positive",
		})
		require.NoError(t, err)
		require.Equal(t, models.GraderKindTrigger, g.Kind())

		result, err := g.Grade(context.Background(), &Context{
			TestCase: &models.TestCase{
				Stimulus: models.TestStimulus{
					Message: "Please deploy this API to Azure and publish it.",
				},
			},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.GreaterOrEqual(t, result.Score, defaultTriggerThreshold)
	})

	t.Run("positive mode fails for unrelated prompt", func(t *testing.T) {
		g, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "positive",
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			TestCase: &models.TestCase{
				Stimulus: models.TestStimulus{
					Message: "Write unit tests for my Go package.",
				},
			},
		})
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Less(t, result.Score, defaultTriggerThreshold)
	})

	t.Run("negative mode passes for unrelated prompt", func(t *testing.T) {
		g, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "negative",
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			TestCase: &models.TestCase{
				Stimulus: models.TestStimulus{
					Message: "Write unit tests for my Go package.",
				},
			},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
	})

	t.Run("negative mode fails for matching prompt", func(t *testing.T) {
		g, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
			SkillPath: skillPath,
			Mode:      "negative",
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			TestCase: &models.TestCase{
				Stimulus: models.TestStimulus{
					Message: "Can you deploy this app to Azure?",
				},
			},
		})
		require.NoError(t, err)
		require.False(t, result.Passed)
	})
}

func TestTriggerHeuristicGrader_ThresholdBoundary(t *testing.T) {
	skillPath := writeTestSkillFile(t)
	threshold := 0.5

	g, err := NewTriggerHeuristicGrader("trigger", models.TriggerHeuristicGraderParameters{
		SkillPath: skillPath,
		Mode:      "positive",
		Threshold: &threshold,
	})
	require.NoError(t, err)

	result, err := g.Grade(context.Background(), &Context{
		TestCase: &models.TestCase{
			Stimulus: models.TestStimulus{
				Message: "deploy azure",
			},
		},
	})
	require.NoError(t, err)
	require.True(t, result.Score >= threshold)
	require.True(t, result.Passed)
}

func TestTriggerHeuristicGrader_ViaCreate(t *testing.T) {
	skillPath := writeTestSkillFile(t)
	threshold := 0.6

	g, err := Create("from-create", models.TriggerHeuristicGraderParameters{
		SkillPath: skillPath,
		Mode:      "positive",
		Threshold: &threshold,
	})
	require.NoError(t, err)
	require.Equal(t, models.GraderKindTrigger, g.Kind())
}

func writeTestSkillFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")

	content := `---
name: azure-deploy
description: "Deploy to Azure resources. USE FOR: deploy to azure, publish api, release to cloud. DO NOT USE FOR: write unit tests."
---
# Azure deploy skill

## Deployment
Use this skill for Azure deployment workflows.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestTriggerHeuristicGrader_NilGradingContext(t *testing.T) {
	g, err := NewTriggerHeuristicGrader("nil-ctx-test", models.TriggerHeuristicGraderParameters{
		SkillPath: writeTestSkillFile(t),
		Mode:      "positive",
	})
	require.NoError(t, err)

	// nil context returns zero score, not an error
	result, err := g.Grade(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, float64(0), result.Score)
	assert.False(t, result.Passed)

	// nil TestCase also returns zero score
	result, err = g.Grade(context.Background(), &Context{})
	require.NoError(t, err)
	assert.Equal(t, float64(0), result.Score)
	assert.False(t, result.Passed)
}

func TestTriggerHeuristicGrader_DuplicateTokensDoNotDiluteScore(t *testing.T) {
	skillPath := writeTestSkillFile(t)
	g, err := NewTriggerHeuristicGrader("dup-test", models.TriggerHeuristicGraderParameters{
		SkillPath: skillPath,
		Mode:      "positive",
	})
	require.NoError(t, err)

	// "deploy deploy deploy" has 1 unique token matching keyword "deploy".
	// With unique-token normalization: 1 match / 1 unique token = 1.0
	// Without normalization: 1 match / 3 tokens = 0.33 (artificially low)
	result, err := g.Grade(context.Background(), &Context{
		TestCase: &models.TestCase{Stimulus: models.TestStimulus{Message: "deploy deploy deploy"}},
	})
	require.NoError(t, err)
	assert.True(t, result.Passed, "repeated keyword prompt should pass")
	assert.Greater(t, result.Score, 0.5, "score should not be diluted by duplicate tokens")
}

func TestTriggerHeuristicGrader_SkillPathDirectory(t *testing.T) {
	// writeTestSkillFile returns the file path; extract the directory
	filePath := writeTestSkillFile(t)
	dirPath := filepath.Dir(filePath)

	g, err := NewTriggerHeuristicGrader("dir-test", models.TriggerHeuristicGraderParameters{
		SkillPath: dirPath,
		Mode:      "positive",
	})
	require.NoError(t, err)

	result, err := g.Grade(context.Background(), &Context{
		TestCase: &models.TestCase{Stimulus: models.TestStimulus{Message: "deploy to azure"}},
	})
	require.NoError(t, err)
	assert.True(t, result.Passed)
}
