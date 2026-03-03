package graders

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestBehaviorGrader_Basic(t *testing.T) {
	g, err := NewBehaviorGrader("test", BehaviorGraderParams{
		MaxToolCalls: 10,
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindBehavior, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestBehaviorGrader_Constructor(t *testing.T) {
	t.Run("empty params returns error", func(t *testing.T) {
		_, err := NewBehaviorGrader("test", BehaviorGraderParams{})
		require.Error(t, err)
	})

	t.Run("valid params succeeds", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 5,
		})
		require.NoError(t, err)
		require.NotNil(t, g)
	})
}

func TestBehaviorGrader_MaxToolCalls(t *testing.T) {
	t.Run("pass when under limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 5,
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("pass when exactly at limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 10,
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("fail when over limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 5,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 8,
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "Tool")
	})

	t.Run("skip when zero (not configured)", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 1000, // need at least one rule
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 999,
				Usage:         &models.UsageStats{InputTokens: 500},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

func TestBehaviorGrader_MaxTokens(t *testing.T) {
	t.Run("pass when under limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 1000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				Usage: &models.UsageStats{InputTokens: 500},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("pass when exactly at limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 1000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				Usage: &models.UsageStats{InputTokens: 1000},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("fail when over limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 500,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				Usage: &models.UsageStats{InputTokens: 1200},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "Token")
	})

	t.Run("pass when usage is nil", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 500,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
	})
}

func TestBehaviorGrader_RequiredTools(t *testing.T) {
	t.Run("pass when all required tools present", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			RequiredTools: []string{"read_file", "write_file"},
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

	t.Run("fail when required tool missing", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			RequiredTools: []string{"read_file", "write_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "search"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "write_file")
	})

	t.Run("fail when no tools used but tools required", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			RequiredTools: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "read_file")
	})
}

func TestBehaviorGrader_ForbiddenTools(t *testing.T) {
	t.Run("pass when no forbidden tools used", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			ForbiddenTools: []string{"exec", "shell"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("fail when forbidden tool used", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			ForbiddenTools: []string{"exec", "shell"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{"read_file", "exec", "write_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "exec")
	})

	t.Run("pass when no tools used and tools forbidden", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			ForbiddenTools: []string{"exec"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: []string{},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

func TestBehaviorGrader_MaxDurationMS(t *testing.T) {
	t.Run("pass when under limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxDurationMS: 5000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 3000,
			Session:    &models.SessionDigest{},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("pass when exactly at limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxDurationMS: 5000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 5000,
			Session:    &models.SessionDigest{},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("fail when over limit", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxDurationMS: 5000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 8000,
			Session:    &models.SessionDigest{},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "Duration")
	})
}

func TestBehaviorGrader_CombinedRules(t *testing.T) {
	t.Run("all rules pass", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls:   10,
			MaxTokens:      2000,
			RequiredTools:  []string{"read_file"},
			ForbiddenTools: []string{"exec"},
			MaxDurationMS:  10000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 5000,
			Session: &models.SessionDigest{
				ToolCallCount: 3,
				Usage:         &models.UsageStats{InputTokens: 800},
				ToolsUsed:     []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("one rule fails among many", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls:   10,
			MaxTokens:      2000,
			RequiredTools:  []string{"read_file"},
			ForbiddenTools: []string{"exec"},
			MaxDurationMS:  10000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 5000,
			Session: &models.SessionDigest{
				ToolCallCount: 15, // over limit
				Usage:         &models.UsageStats{InputTokens: 800},
				ToolsUsed:     []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// Score should be partial: 4 of 5 rules passed
		require.Less(t, results.Score, 1.0)
		require.Greater(t, results.Score, 0.0)
	})

	t.Run("multiple rules fail", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls:   5,
			MaxTokens:      1000,
			RequiredTools:  []string{"read_file"},
			ForbiddenTools: []string{"exec"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 20,                                    // over limit
				Usage:         &models.UsageStats{InputTokens: 5000}, // over limit
				ToolsUsed:     []string{"exec"},                      // forbidden used, required missing
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Less(t, results.Score, 0.5)
	})

	t.Run("rules checked independently", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 5,
			MaxTokens:    1000,
		})
		require.NoError(t, err)

		// tool calls over, tokens under
		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 10,
				Usage:         &models.UsageStats{InputTokens: 500},
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score) // 1 of 2 passed
	})
}

func TestBehaviorGrader_EdgeCases(t *testing.T) {
	t.Run("nil session returns error or fails gracefully", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: nil,
		})
		// Either an error or a graceful failure is acceptable
		if err != nil {
			require.Contains(t, err.Error(), "session")
		} else {
			require.False(t, results.Passed)
			require.Equal(t, 0.0, results.Score)
		}
	})

	t.Run("zero tool calls with max_tool_calls set passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 0,
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("zero tokens with max_tokens set passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxTokens: 500,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				Usage: &models.UsageStats{InputTokens: 0},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("empty required_tools list passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10, // need at least one rule
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 3,
				ToolsUsed:     []string{},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
	})

	t.Run("empty forbidden_tools list passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10, // need at least one rule
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 3,
				ToolsUsed:     []string{"exec", "shell"},
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
	})

	t.Run("result details contains expected fields", func(t *testing.T) {
		g, err := NewBehaviorGrader("detail-test", BehaviorGraderParams{
			MaxToolCalls:   10,
			RequiredTools:  []string{"read_file"},
			ForbiddenTools: []string{"exec"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 3,
				ToolsUsed:     []string{"read_file", "write_file"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindBehavior, results.Type)
		require.True(t, results.Passed)
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxToolCalls: 10,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolCallCount: 3,
			},
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})

	t.Run("zero duration with max_duration_ms passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			MaxDurationMS: 5000,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			DurationMS: 0,
			Session:    &models.SessionDigest{},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("nil tools_used with required_tools fails", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			RequiredTools: []string{"read_file"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: nil,
			},
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "read_file")
	})

	t.Run("nil tools_used with forbidden_tools passes", func(t *testing.T) {
		g, err := NewBehaviorGrader("test", BehaviorGraderParams{
			ForbiddenTools: []string{"exec"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Session: &models.SessionDigest{
				ToolsUsed: nil,
			},
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure BehaviorGrader satisfies the Grader interface at compile time.
var _ Grader = (*behaviorGrader)(nil)
