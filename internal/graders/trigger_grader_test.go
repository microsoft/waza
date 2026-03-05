package graders

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestTriggerHeuristicGrader_ConstructorValidation(t *testing.T) {
	skillPath := writeTestSkillFile(t)

	t.Run("missing skill_path returns error", func(t *testing.T) {
		_, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
			Mode: "positive",
		})
		require.Error(t, err)
	})

	t.Run("invalid mode returns error", func(t *testing.T) {
		_, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
			SkillPath: skillPath,
			Mode:      "maybe",
		})
		require.Error(t, err)
	})

	t.Run("invalid threshold returns error", func(t *testing.T) {
		threshold := 1.2
		_, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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
		g, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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
		g, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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
		g, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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
		g, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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

	g, err := NewTriggerHeuristicGrader("trigger", TriggerHeuristicGraderParams{
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

	g, err := Create(models.GraderKindTrigger, "from-create", map[string]any{
		"skill_path": skillPath,
		"mode":       "positive",
		"threshold":  0.6,
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
