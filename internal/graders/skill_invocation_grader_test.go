package graders

import (
	"context"
	"testing"

	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Constructor & metadata
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_Basic(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
		Mode:           "exact_match",
		RequiredSkills: []string{"azure-prepare", "azure-deploy"},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindSkillInvocation, g.Kind())
	require.Equal(t, "test", g.Name())
	require.True(t, g.allowExtra) // default value
}

func TestSkillInvocationGrader_Constructor(t *testing.T) {
	t.Run("no required_skills returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: nil,
		})
		require.Error(t, err)
	})

	t.Run("empty required_skills returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: []string{},
		})
		require.Error(t, err)
	})

	t.Run("invalid mode returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "invalid_mode",
			RequiredSkills: []string{"skill1"},
		})
		require.Error(t, err)
	})

	t.Run("valid exact_match params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
		require.True(t, g.allowExtra) // default
	})

	t.Run("valid in_order params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "in_order",
			RequiredSkills: []string{"skill1", "skill2"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("valid any_order params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "any_order",
			RequiredSkills: []string{"skill1", "skill2"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("allow_extra flag can be set to false", func(t *testing.T) {
		allowExtra := false
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
			AllowExtra:     &allowExtra,
		})
		require.NoError(t, err)
		require.False(t, g.allowExtra)
	})
}

// ---------------------------------------------------------------------------
// exact_match mode
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_ExactMatch(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
		Mode:           "exact_match",
		RequiredSkills: []string{"azure-prepare", "azure-deploy"},
	})
	require.NoError(t, err)

	t.Run("exact match passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
				{Name: "azure-deploy"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
		require.Contains(t, result.Feedback, "matched")
	})

	t.Run("extra skill fails exact match", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
				{Name: "azure-deploy"},
				{Name: "azure-monitor"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Less(t, result.Score, 1.0)
	})

	t.Run("missing skill fails", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Less(t, result.Score, 1.0)
	})

	t.Run("wrong order fails exact match", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-deploy"},
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
	})

	t.Run("empty invocations fails", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Equal(t, 0.0, result.Score)
	})
}

// ---------------------------------------------------------------------------
// in_order mode
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_InOrder(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
		Mode:           "in_order",
		RequiredSkills: []string{"azure-prepare", "azure-deploy"},
	})
	require.NoError(t, err)

	t.Run("exact sequence passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
				{Name: "azure-deploy"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
	})

	t.Run("in order with extras passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
				{Name: "azure-validate"},
				{Name: "azure-deploy"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Greater(t, result.Score, 0.6) // High score but not perfect due to extra
	})

	t.Run("out of order fails", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-deploy"},
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
	})

	t.Run("missing required skill fails", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
	})
}

// ---------------------------------------------------------------------------
// any_order mode
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_AnyOrder(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
		Mode:           "any_order",
		RequiredSkills: []string{"azure-prepare", "azure-deploy"},
	})
	require.NoError(t, err)

	t.Run("all skills present in order passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
				{Name: "azure-deploy"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
	})

	t.Run("all skills present out of order passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-deploy"},
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
	})

	t.Run("all skills with extras passes", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-validate"},
				{Name: "azure-prepare"},
				{Name: "azure-deploy"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Greater(t, result.Score, 0.6) // High score but not perfect due to extra
	})

	t.Run("missing skill fails", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "azure-prepare"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
	})

	t.Run("duplicate required skills handled correctly", func(t *testing.T) {
		g2, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "any_order",
			RequiredSkills: []string{"skill1", "skill1", "skill2"},
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill1"},
				{Name: "skill2"},
			},
		}

		result, err := g2.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
	})
}

// ---------------------------------------------------------------------------
// allow_extra flag
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_AllowExtra(t *testing.T) {
	t.Run("allow_extra=true does not penalize extras", func(t *testing.T) {
		allowExtra := true
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "in_order",
			RequiredSkills: []string{"skill1"},
			AllowExtra:     &allowExtra,
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill2"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		// Score is not perfectly 1.0 due to precision calculation
		require.Greater(t, result.Score, 0.5)
	})

	t.Run("allow_extra=false penalizes extras", func(t *testing.T) {
		allowExtra := false
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "in_order",
			RequiredSkills: []string{"skill1"},
			AllowExtra:     &allowExtra,
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill2"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed) // Still passes the match, but score is reduced
		// Score should be reduced due to penalty - F1 would be ~0.666, with penalty should be lower
		require.Less(t, result.Score, 0.6)
		require.Contains(t, result.Feedback, "extra invocations")
	})
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_EdgeCases(t *testing.T) {
	t.Run("nil skill invocations treated as empty", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "any_order",
			RequiredSkills: []string{"skill1"},
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: nil,
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Equal(t, 0.0, result.Score)
	})

	t.Run("empty skill names are extracted", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: ""},
				{Name: "skill1"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed) // Extra empty name causes failure in exact match
	})

	t.Run("details contain expected fields", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
		})
		require.NoError(t, err)

		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)

		require.Contains(t, result.Details, "mode")
		require.Contains(t, result.Details, "required_skills")
		require.Contains(t, result.Details, "actual_skills")
		require.Contains(t, result.Details, "allow_extra")
		require.Contains(t, result.Details, "precision")
		require.Contains(t, result.Details, "recall")
		require.Contains(t, result.Details, "f1")
	})
}

// ---------------------------------------------------------------------------
// Precision/Recall calculations
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_PrecisionRecall(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", SkillInvocationGraderParams{
		Mode:           "any_order",
		RequiredSkills: []string{"skill1", "skill2"},
	})
	require.NoError(t, err)

	t.Run("perfect match has precision=1.0 recall=1.0", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill2"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.Equal(t, 1.0, result.Details["precision"])
		require.Equal(t, 1.0, result.Details["recall"])
		require.Equal(t, 1.0, result.Details["f1"])
	})

	t.Run("missing skill reduces recall", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.Equal(t, 1.0, result.Details["precision"]) // All actual are in required
		require.Equal(t, 0.5, result.Details["recall"])    // Only 1 of 2 required found
		require.Greater(t, result.Details["f1"], 0.0)
		require.Less(t, result.Details["f1"], 1.0)
	})

	t.Run("extra skill reduces precision", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill2"},
				{Name: "skill3"},
			},
		}

		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.Less(t, result.Details["precision"].(float64), 1.0) // Not all actual are in required
		require.Equal(t, 1.0, result.Details["recall"])              // All required found
	})
}
