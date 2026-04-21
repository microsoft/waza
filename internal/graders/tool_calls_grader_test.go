package graders

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestToolCallsGrader_NoConstraints(t *testing.T) {
	_, err := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one constraint")
}

func TestToolCallsGrader_NegativeMinCalls(t *testing.T) {
	_, err := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{MinCalls: intPtr(-1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_calls must be non-negative")
}

func TestToolCallsGrader_NegativeMaxCalls(t *testing.T) {
	_, err := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{MaxCalls: intPtr(-1)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_calls must be non-negative")
}

func TestToolCallsGrader_MinGreaterThanMax(t *testing.T) {
	_, err := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MinCalls: intPtr(5),
		MaxCalls: intPtr(2),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_calls (5) must be <= max_calls (2)")
}

func TestToolCallsGrader_ValidMinEqualsMax(t *testing.T) {
	g, err := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MinCalls: intPtr(3),
		MaxCalls: intPtr(3),
	})
	require.NoError(t, err)
	require.NotNil(t, g)
}

func TestToolCallsGrader_NameAndKind(t *testing.T) {
	g, err := NewToolCallsGrader("my-grader", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
	})
	require.NoError(t, err)
	require.Equal(t, "my-grader", g.Name())
	require.Equal(t, models.GraderKindToolCalls, g.Kind())
}

func TestToolCallsGrader_NilSession(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
	})
	res, err := g.Grade(context.Background(), &Context{})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, float64(0), res.Score)
	require.Contains(t, res.Feedback, "no session data")
}

func TestToolCallsGrader_RequiredTools_AllPresent(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash", "view"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
				{Name: "edit"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Equal(t, 1.0, res.Score)
}

func TestToolCallsGrader_RequiredTools_SomeMissing(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash", "view", "grep"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.InDelta(t, 1.0/3.0, res.Score, 0.01)
	require.Contains(t, res.Feedback, "view")
	require.Contains(t, res.Feedback, "grep")
}

func TestToolCallsGrader_ForbiddenTools_NoneUsed(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		ForbiddenTools: []string{"rm", "sudo"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Equal(t, 1.0, res.Score)
}

func TestToolCallsGrader_ForbiddenTools_SomeUsed(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		ForbiddenTools: []string{"rm", "sudo"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "rm"},
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, 0.5, res.Score)
	require.Contains(t, res.Feedback, `forbidden tool "rm" was called`)
}

func TestToolCallsGrader_MinCalls_Pass(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MinCalls: intPtr(2),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
}

func TestToolCallsGrader_MinCalls_Fail(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MinCalls: intPtr(5),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Contains(t, res.Feedback, "at least 5 tool calls")
}

func TestToolCallsGrader_MaxCalls_Pass(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MaxCalls: intPtr(3),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
}

func TestToolCallsGrader_MaxCalls_Fail(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MaxCalls: intPtr(1),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
				{Name: "edit"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Contains(t, res.Feedback, "at most 1 tool calls")
}

func TestToolCallsGrader_Combined_AllPass(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools:  []string{"bash"},
		ForbiddenTools: []string{"rm"},
		MinCalls:       intPtr(1),
		MaxCalls:       intPtr(5),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Equal(t, 1.0, res.Score)
	require.Contains(t, res.Feedback, "all tool_calls checks passed")
}

func TestToolCallsGrader_Combined_PartialFailure(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools:  []string{"bash", "grep"},
		ForbiddenTools: []string{"rm"},
		MinCalls:       intPtr(1),
		MaxCalls:       intPtr(10),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "view"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.InDelta(t, 4.0/5.0, res.Score, 0.01)
	require.Contains(t, res.Feedback, `required tool "grep" was not called`)
}

func TestToolCallsGrader_AllChecksFail(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools:  []string{"bash"},
		ForbiddenTools: []string{"rm"},
		MinCalls:       intPtr(10),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "rm"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, 0.0, res.Score)
}

func TestToolCallsGrader_Details(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "bash"},
				{Name: "view"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, res.Details["total_calls"])
	require.Equal(t, 1, res.Details["passed_checks"])
	require.Equal(t, 1, res.Details["total_checks"])
	unique, ok := res.Details["unique_tools"].([]string)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"bash", "view"}, unique)
}

func TestToolCallsGrader_EmptyToolCalls(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
	require.Equal(t, 0.0, res.Score)
}

func TestToolCallsGrader_DuplicateToolCalls(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
		MaxCalls:      intPtr(5),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
				{Name: "bash"},
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
	require.Equal(t, 3, res.Details["total_calls"])
}

func TestToolCallsGrader_MinCallsZero(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MinCalls: intPtr(0),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{},
		},
	})
	require.NoError(t, err)
	require.True(t, res.Passed)
}

func TestToolCallsGrader_MaxCallsZero(t *testing.T) {
	g, _ := NewToolCallsGrader("tc", models.ToolCallsGraderParameters{
		MaxCalls: intPtr(0),
	})
	res, err := g.Grade(context.Background(), &Context{
		Session: &models.SessionDigest{
			ToolCalls: []models.ToolCall{
				{Name: "bash"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.Passed)
}

func TestToolCallsGrader_Factory(t *testing.T) {
	g, err := Create("check-tools", models.ToolCallsGraderParameters{
		RequiredTools: []string{"bash"},
	})
	require.NoError(t, err)
	require.Equal(t, "check-tools", g.Name())
	require.Equal(t, models.GraderKindToolCalls, g.Kind())
}
