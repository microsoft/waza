package graders

import (
	"context"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestKeywordGrader_Basic(t *testing.T) {
	g, err := NewKeywordGrader(KeywordGraderArgs{
		Name:        "test",
		MustContain: []string{"hello"},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindKeyword, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestKeywordGrader_Grade(t *testing.T) {
	t.Run("all must_contain keywords found", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:        "test",
			MustContain: []string{"hello", "world"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "All keyword checks passed", results.Feedback)
	})

	t.Run("must_contain keyword missing", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:        "test",
			MustContain: []string{"hello", "missing"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Missing expected keyword: missing")
	})

	t.Run("case-insensitive matching", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:        "test",
			MustContain: []string{"HELLO", "World"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "Hello World",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("must_not_contain passes when keyword absent", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:           "test",
			MustNotContain: []string{"error", "fail"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "all good here",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("must_not_contain fails when forbidden keyword found", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:           "test",
			MustNotContain: []string{"error", "warning"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "found an ERROR in output",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Found forbidden keyword: error")
	})

	t.Run("combined must_contain and must_not_contain all pass", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:           "test",
			MustContain:    []string{"func"},
			MustNotContain: []string{"panic"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "func main() {}",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("combined partial failure", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:           "test",
			MustContain:    []string{"func", "return"},
			MustNotContain: []string{"panic", "os.Exit"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "func main() { panic(\"boom\") }",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// 2 of 4 checks fail: missing "return", found "panic"
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Missing expected keyword: return")
		require.Contains(t, results.Feedback, "Found forbidden keyword: panic")
	})

	t.Run("no keywords yields score 1 and passes", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{Name: "test"})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "anything",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("empty output fails must_contain", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:        "test",
			MustContain: []string{"something"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("result details contains expected fields", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:           "detail-test",
			MustContain:    []string{"a"},
			MustNotContain: []string{"z"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "abc",
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindKeyword, results.Type)
		require.Equal(t, []string{"a"}, results.Details["must_contain"])
		require.Equal(t, []string{"z"}, results.Details["must_not_contain"])
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewKeywordGrader(KeywordGraderArgs{
			Name:        "test",
			MustContain: []string{"ok"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "ok",
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

func TestKeywordGrader_ViaCreate(t *testing.T) {
	t.Run("Create with GraderKindKeyword works", func(t *testing.T) {
		g, err := Create(models.GraderKindKeyword, "from-create", map[string]any{
			"must_contain":     []string{"hello"},
			"must_not_contain": []string{"bye"},
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindKeyword, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("Create with keywords alias works", func(t *testing.T) {
		g, err := Create(models.GraderKindKeyword, "from-alias", map[string]any{
			"keywords": []string{"hello"},
		})
		require.NoError(t, err)
		require.Equal(t, "from-alias", g.Name())

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure keywordGrader satisfies the Grader interface at compile time.
var _ Grader = (*keywordGrader)(nil)
