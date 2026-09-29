package graders

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Constructor & metadata
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_Basic(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
		Mode:           "exact_match",
		RequiredSkills: []string{"azure-prepare", "azure-deploy"},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindSkillInvocation, g.Kind())
	require.Equal(t, "test", g.Name())
	require.True(t, g.allowExtra) // default value
}

func TestSkillInvocationGraderParams_ConfigRoundTrip(t *testing.T) {
	t.Run("roundtrip_allow_extra_omitted", func(t *testing.T) {
		original := models.SkillInvocationGraderParameters{
			RequiredSkills: []string{"azure-prepare", "azure-deploy"},
			Mode:           models.SkillMatchingModeAnyOrder,
		}

		raw, err := yaml.Marshal(original)
		require.NoError(t, err)

		config := map[string]any{}
		require.NoError(t, yaml.Unmarshal(raw, &config))
		require.NotContains(t, config, "allow_extra")

		var decoded models.SkillInvocationGraderParameters
		require.NoError(t, yaml.Unmarshal(raw, &decoded))
		require.Equal(t, original, decoded)
	})

	t.Run("roundtrip_allow_extra_set", func(t *testing.T) {
		allowExtra := false
		original := models.SkillInvocationGraderParameters{
			RequiredSkills: []string{"azure-prepare", "azure-deploy"},
			Mode:           models.SkillMatchingModeExact,
			AllowExtra:     &allowExtra,
		}

		raw, err := yaml.Marshal(original)
		require.NoError(t, err)

		config := map[string]any{}
		require.NoError(t, yaml.Unmarshal(raw, &config))
		require.Contains(t, config, "allow_extra")
		require.Equal(t, false, config["allow_extra"])

		var decoded models.SkillInvocationGraderParameters
		require.NoError(t, yaml.Unmarshal(raw, &decoded))
		require.Equal(t, original, decoded)
	})

	t.Run("roundtrip_forbidden_skills", func(t *testing.T) {
		original := models.SkillInvocationGraderParameters{
			ForbiddenSkills: []string{"azure-deploy"},
		}

		raw, err := yaml.Marshal(original)
		require.NoError(t, err)

		config := map[string]any{}
		require.NoError(t, yaml.Unmarshal(raw, &config))
		require.Contains(t, config, "forbidden_skills")
		require.NotContains(t, config, "required_skills")
		require.NotContains(t, config, "mode")

		var decoded models.SkillInvocationGraderParameters
		require.NoError(t, yaml.Unmarshal(raw, &decoded))
		require.Equal(t, original, decoded)
	})

	t.Run("roundtrip_empty_fields_omitted", func(t *testing.T) {
		original := models.SkillInvocationGraderParameters{}

		raw, err := yaml.Marshal(original)
		require.NoError(t, err)

		config := map[string]any{}
		require.NoError(t, yaml.Unmarshal(raw, &config))
		require.NotContains(t, config, "required_skills")
		require.NotContains(t, config, "forbidden_skills")
		require.NotContains(t, config, "mode")
		require.NotContains(t, config, "allow_extra")

		var decoded models.SkillInvocationGraderParameters
		require.NoError(t, yaml.Unmarshal(raw, &decoded))
		require.Equal(t, original, decoded)
	})
}

func TestSkillInvocationGrader_Constructor(t *testing.T) {
	t.Run("no skill constraints returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "exact_match",
			RequiredSkills: nil,
		})
		require.Error(t, err)
	})

	t.Run("empty skill constraints returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "exact_match",
			RequiredSkills: []string{},
		})
		require.Error(t, err)
	})

	t.Run("invalid mode returns error", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "invalid_mode",
			RequiredSkills: []string{"skill1"},
		})
		require.Error(t, err)
	})

	t.Run("valid exact_match params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
		require.True(t, g.allowExtra) // default
	})

	t.Run("valid in_order params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "in_order",
			RequiredSkills: []string{"skill1", "skill2"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("valid any_order params succeeds", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "any_order",
			RequiredSkills: []string{"skill1", "skill2"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("allow_extra flag can be set to false", func(t *testing.T) {
		allowExtra := false
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:           "exact_match",
			RequiredSkills: []string{"skill1"},
			AllowExtra:     &allowExtra,
		})
		require.NoError(t, err)
		require.False(t, g.allowExtra)
	})

	t.Run("forbidden_skills only succeeds without mode", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			ForbiddenSkills: []string{"skill1"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
		require.Equal(t, models.SkillMatchingModeAnyOrder, g.matchingMode)
	})

	t.Run("forbidden_skills only validates explicit mode", func(t *testing.T) {
		_, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:            "invalid_mode",
			ForbiddenSkills: []string{"skill1"},
		})
		require.Error(t, err)
	})
}

// ---------------------------------------------------------------------------
// exact_match mode
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_ExactMatch(t *testing.T) {
	g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
	g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
	g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		g2, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
// forbidden_skills
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_ForbiddenSkills(t *testing.T) {
	t.Run("forbidden-only passes with no invocations", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			ForbiddenSkills: []string{"skill1"},
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			SkillInvocations: []execution.SkillInvocation{},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
		require.Equal(t, []string{}, result.Details["forbidden_violations"])
	})

	t.Run("forbidden-only allows unrelated skills", func(t *testing.T) {
		allowExtra := false
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			ForbiddenSkills: []string{"skill1"},
			AllowExtra:      &allowExtra,
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill2"},
			},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
	})

	t.Run("forbidden-only fails when forbidden skill appears", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			ForbiddenSkills: []string{"skill1"},
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
			},
		})
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Equal(t, 0.0, result.Score)
		require.Equal(t, []string{"skill1"}, result.Details["forbidden_violations"])
		require.Contains(t, result.Feedback, "Forbidden skills invoked: skill1")
	})

	t.Run("mixed required and forbidden passes when required appears and forbidden is absent", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:            "any_order",
			RequiredSkills:  []string{"skill1"},
			ForbiddenSkills: []string{"skill3"},
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill2"},
			},
		})
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Greater(t, result.Score, 0.0)
		require.Equal(t, []string{}, result.Details["forbidden_violations"])
	})

	t.Run("mixed required and forbidden fails when forbidden appears", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
			Mode:            "any_order",
			RequiredSkills:  []string{"skill1"},
			ForbiddenSkills: []string{"skill3"},
		})
		require.NoError(t, err)

		result, err := g.Grade(context.Background(), &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "skill1"},
				{Name: "skill3"},
			},
		})
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Equal(t, 0.0, result.Score)
		require.Equal(t, []string{"skill3"}, result.Details["forbidden_violations"])
		require.Contains(t, result.Feedback, "Forbidden skills invoked: skill3")
		require.NotContains(t, result.Feedback, "Any-order match failed")
	})
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_EdgeCases(t *testing.T) {
	t.Run("nil skill invocations treated as empty", func(t *testing.T) {
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		require.Contains(t, result.Details, "forbidden_skills")
		require.Contains(t, result.Details, "forbidden_violations")
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
	g, err := NewSkillInvocationGrader("test", models.SkillInvocationGraderParameters{
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
		precision, ok := result.Details["precision"].(float64)
		require.True(t, ok)
		require.Less(t, precision, 1.0)                 // Not all actual are in required
		require.Equal(t, 1.0, result.Details["recall"]) // All required found
	})
}

// ---------------------------------------------------------------------------
// Routing-surface telemetry (issue #540)
// ---------------------------------------------------------------------------

func TestSkillInvocationGrader_SurfacingClassification(t *testing.T) {
	g, err := NewSkillInvocationGrader("must_invoke", models.SkillInvocationGraderParameters{
		Mode:           models.SkillMatchingModeAnyOrder,
		RequiredSkills: []string{"prioritize-features"},
	})
	require.NoError(t, err)

	t.Run("surfaced but not invoked distinguishes description-loss from missing-catalog", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{},
			AvailableSkills: []execution.AvailableSkill{
				{Name: "prioritize-features", Path: "/skills/prioritize-features/SKILL.md"},
				{Name: "pre-mortem", Path: "/skills/pre-mortem/SKILL.md"},
			},
			AvailableSkillsKnown: true,
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Contains(t, result.Feedback, "surfaced but not invoked: prioritize-features")
		require.NotContains(t, result.Feedback, "runtime never surfaced")

		surfacedButNot, ok := result.Details["surfaced_but_not_invoked_required_skills"].([]string)
		require.True(t, ok)
		require.Equal(t, []string{"prioritize-features"}, surfacedButNot)
		notSurfaced, ok := result.Details["not_surfaced_required_skills"].([]string)
		require.True(t, ok)
		require.Empty(t, notSurfaced)
		available, ok := result.Details["available_skills"].([]string)
		require.True(t, ok)
		require.ElementsMatch(t, []string{"prioritize-features", "pre-mortem"}, available)
	})

	t.Run("not surfaced identifies runtime-side regressions", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{},
			AvailableSkills: []execution.AvailableSkill{
				{Name: "pre-mortem", Path: "/skills/pre-mortem/SKILL.md"},
			},
			AvailableSkillsKnown: true,
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Contains(t, result.Feedback, "runtime never surfaced: prioritize-features")
		require.NotContains(t, result.Feedback, "surfaced but not invoked")

		notSurfaced, ok := result.Details["not_surfaced_required_skills"].([]string)
		require.True(t, ok)
		require.Equal(t, []string{"prioritize-features"}, notSurfaced)
		surfacedButNot, ok := result.Details["surfaced_but_not_invoked_required_skills"].([]string)
		require.True(t, ok)
		require.Empty(t, surfacedButNot)
	})

	t.Run("known-empty catalog identifies every missing required skill as not surfaced", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations:     []execution.SkillInvocation{},
			AvailableSkills:      []execution.AvailableSkill{},
			AvailableSkillsKnown: true,
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Contains(t, result.Feedback, "runtime never surfaced: prioritize-features")
		require.Equal(t, []string{}, result.Details["available_skills"])
		require.Equal(t, []string{"prioritize-features"}, result.Details["not_surfaced_required_skills"])
		require.Equal(t, []string{}, result.Details["surfaced_but_not_invoked_required_skills"])
	})

	t.Run("legacy responses without available skills preserve prior feedback", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{},
			// AvailableSkills intentionally nil to simulate legacy / non-Copilot
			// executors that do not populate the routing catalog snapshot.
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.NotContains(t, result.Feedback, "runtime never surfaced")
		require.NotContains(t, result.Feedback, "surfaced but not invoked")
		_, hasKey := result.Details["available_skills"]
		require.False(t, hasKey, "available_skills details must be omitted when AvailableSkills is nil")
	})

	t.Run("non-empty snapshots written before the known flag remain classifiable", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{},
			AvailableSkills: []execution.AvailableSkill{
				{Name: "prioritize-features", Path: "/skills/prioritize-features/SKILL.md"},
			},
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.False(t, result.Passed)
		require.Contains(t, result.Feedback, "surfaced but not invoked: prioritize-features")
	})

	t.Run("passing runs are unaffected by surfacing telemetry", func(t *testing.T) {
		ctx := &Context{
			SkillInvocations: []execution.SkillInvocation{
				{Name: "prioritize-features"},
			},
			AvailableSkills: []execution.AvailableSkill{
				{Name: "prioritize-features", Path: "/skills/prioritize-features/SKILL.md"},
			},
			AvailableSkillsKnown: true,
		}
		result, err := g.Grade(context.Background(), ctx)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Equal(t, 1.0, result.Score)
		require.Equal(t, "Skill invocation sequence matched", result.Feedback)
	})
}
