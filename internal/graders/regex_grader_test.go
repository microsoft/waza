package graders

import (
	"context"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRegexGrader_Basic(t *testing.T) {
	g, err := NewRegexGrader("test", []string{`he.*`, `world`}, nil)
	require.NoError(t, err)

	require.Equal(t, models.GraderKindRegex, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestRegexGrader_Grade(t *testing.T) {
	t.Run("all must_match patterns match", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`he.*`, `world`}, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "All patterns matched", results.Feedback)
	})

	t.Run("must_match pattern missing", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`hello`, `missing`}, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Missing expected pattern: missing")
	})

	t.Run("must_not_match passes when pattern absent", func(t *testing.T) {
		g, err := NewRegexGrader("test", nil, []string{`err.*`, `fail`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "all good here",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("must_not_match fails when forbidden pattern found", func(t *testing.T) {
		g, err := NewRegexGrader("test", nil, []string{`error`, `warning`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "found an error in output",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Found forbidden pattern: error")
	})

	t.Run("combined must_match and must_not_match all pass", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`func`}, []string{`panic`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "func main() {}",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("combined must_match and must_not_match partial failure", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`func`, `return`}, []string{`panic`, `os\.Exit`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "func main() { panic(\"boom\") }",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// 2 of 4 checks fail: missing "return", found "panic"
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Missing expected pattern: return")
		require.Contains(t, results.Feedback, "Found forbidden pattern: panic")
	})

	t.Run("invalid must_match regex reports failure", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`[invalid`}, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "anything",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "Invalid 'must_match' regex pattern \"[invalid\"")
	})

	t.Run("invalid must_not_match regex reports failure", func(t *testing.T) {
		g, err := NewRegexGrader("test", nil, []string{`[invalid`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "anything",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "Invalid 'must_not_match' regex pattern \"[invalid\"")
	})

	t.Run("no patterns yields score 1 and passes", func(t *testing.T) {
		g, err := NewRegexGrader("test", nil, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "anything",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("empty output fails must_match", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`something`}, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("result details contains expected fields", func(t *testing.T) {
		g, err := NewRegexGrader("detail-test", []string{`a`}, []string{`z`})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "abc",
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindRegex, results.Type)
		require.Equal(t, []string{"a"}, results.Details["must_match"])
		require.Equal(t, []string{"z"}, results.Details["must_not_match"])
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewRegexGrader("test", []string{`ok`}, nil)
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "ok",
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

func TestRegexGrader_ViaCreate(t *testing.T) {
	t.Run("Create with GraderKindRegex works", func(t *testing.T) {
		g, err := Create(models.GraderKindRegex, "from-create", map[string]any{
			"must_match":     []string{`hello`},
			"must_not_match": []string{`bye`},
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindRegex, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure RegexGrader satisfies the Grader interface at compile time.
var _ Grader = (*RegexGrader)(nil)
var _ *models.GraderResults // ensure import is used
