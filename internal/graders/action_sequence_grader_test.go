package graders

import (
	"context"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Constructor & metadata
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_Basic(t *testing.T) {
	g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
		MatchingMode:    "exact_match",
		ExpectedActions: []string{"read_file", "write_file"},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindActionSequence, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestActionSequenceGrader_Constructor(t *testing.T) {
	t.Run("no expected_actions returns error", func(t *testing.T) {
		_, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: nil,
		})
		require.Error(t, err)
	})

	t.Run("empty expected_actions returns error", func(t *testing.T) {
		_, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{},
		})
		require.Error(t, err)
	})

	t.Run("invalid matching_mode returns error", func(t *testing.T) {
		_, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "invalid_mode",
			ExpectedActions: []string{"read_file"},
		})
		require.Error(t, err)
	})

	t.Run("valid exact_match params succeeds", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("valid in_order_match params succeeds", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})

	t.Run("valid any_order_match params succeeds", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})
}

// ---------------------------------------------------------------------------
// exact_match mode
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_ExactMatch(t *testing.T) {
	t.Run("exact match passes when actual equals expected", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file", "write_file", "search"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file", "search"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("exact match fails with extra step in actual", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file", "search"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 1.0)
	})

	t.Run("exact match fails with missing step in actual", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file", "write_file", "search"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 1.0)
	})

	t.Run("exact match fails with wrong order", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"write_file", "read_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
	})

	t.Run("exact match with empty actual tools", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("exact match with single tool", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// ---------------------------------------------------------------------------
// in_order_match mode
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_InOrderMatch(t *testing.T) {
	t.Run("in-order match passes with exact sequence", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "write_file", "search"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file", "search"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("in-order match passes with extra steps interspersed", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"init", "read_file", "search", "write_file", "cleanup"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		// F1 < 1.0 because extra steps reduce precision; but match still passes
		require.Greater(t, results.Score, 0.0)
	})

	t.Run("in-order match fails with wrong order of expected steps", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"write_file", "read_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// F1 can still be 1.0 (all tools present) even when order is wrong
		// Passed is false because in_order_match requires correct ordering
	})

	t.Run("in-order match fails with missing expected step", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "write_file", "deploy"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 1.0)
	})

	t.Run("in-order match passes when expected is subset in correct order", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "in_order_match",
			ExpectedActions: []string{"read_file", "deploy"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"init", "read_file", "write_file", "test", "deploy", "cleanup"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		// F1 < 1.0 because extra steps reduce precision; but match still passes
		require.Greater(t, results.Score, 0.0)
	})
}

// ---------------------------------------------------------------------------
// any_order_match mode
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_AnyOrderMatch(t *testing.T) {
	t.Run("any-order match passes with same tools different order", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "write_file", "search"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"search", "write_file", "read_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("any-order match passes with extra tools present", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"init", "write_file", "search", "read_file", "cleanup"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		// F1 < 1.0 because extra steps reduce precision; but match still passes
		require.Greater(t, results.Score, 0.0)
	})

	t.Run("any-order match fails with missing expected tool", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "write_file", "deploy"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 1.0)
	})

	t.Run("any-order match with duplicate tools expected", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "read_file", "write_file"},
		})
		require.NoError(t, err)

		// Actual has 2x read_file — should pass
		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"write_file", "read_file", "read_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("any-order match with insufficient duplicates fails", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"read_file", "read_file", "write_file"},
		})
		require.NoError(t, err)

		// Actual has only 1x read_file — should fail
		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"write_file", "read_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 1.0)
	})
}

// ---------------------------------------------------------------------------
// Score calculations
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_ScoreCalculations(t *testing.T) {
	t.Run("perfect match yields score 1.0 and passed true", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"a", "b", "c"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"a", "b", "c"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("partial match yields score between 0 and 1", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"a", "b", "c", "d"},
		})
		require.NoError(t, err)

		// Only 2 of 4 expected tools present
		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"a", "c"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Greater(t, results.Score, 0.0)
		require.Less(t, results.Score, 1.0)
	})

	t.Run("no match yields score 0.0 and passed false", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"a", "b"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"x", "y"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("details contain precision recall f1_score and matching_mode", func(t *testing.T) {
		g, err := NewActionSequenceGrader("detail-test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"a", "b"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"a", "b"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindActionSequence, results.Type)

		require.Contains(t, results.Details, "precision")
		require.Contains(t, results.Details, "recall")
		require.Contains(t, results.Details, "f1")
		require.Contains(t, results.Details, "matching_mode")
		require.Equal(t, "any_order_match", results.Details["matching_mode"])
	})

	t.Run("f1 score is used as the result Score", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "any_order_match",
			ExpectedActions: []string{"a", "b"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"a", "b"},
			},
		})
		require.NoError(t, err)

		f1, ok := results.Details["f1"].(float64)
		require.True(t, ok, "f1 should be a float64")
		require.Equal(t, f1, results.Score)
	})
}

// ---------------------------------------------------------------------------
// Edge / error cases
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_EdgeCases(t *testing.T) {
	t.Run("nil session returns graceful zero-score", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: nil,
		})
		require.NoError(t, err)
		require.NotNil(t, results)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "no session digest")
	})

	t.Run("nil tools_used treated as empty", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: nil,
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewActionSequenceGrader("test", ActionSequenceGraderParams{
			MatchingMode:    "exact_match",
			ExpectedActions: []string{"a"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"a"},
			},
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

// ---------------------------------------------------------------------------
// Factory integration (Create)
// ---------------------------------------------------------------------------

func TestActionSequenceGrader_ViaCreate(t *testing.T) {
	t.Run("Create with GraderKindActionSequence works", func(t *testing.T) {
		g, err := Create(models.GraderKindActionSequence, "from-create", map[string]any{
			"matching_mode":    "exact_match",
			"expected_actions": []string{"read_file", "write_file"},
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindActionSequence, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("Create with invalid params returns error", func(t *testing.T) {
		_, err := Create(models.GraderKindActionSequence, "bad", map[string]any{
			"matching_mode":    "invalid",
			"expected_actions": []string{"a"},
		})
		require.Error(t, err)
	})
}

// Compile-time interface check (unexported struct — use pointer).
var _ Grader = (*actionSequenceGrader)(nil)
