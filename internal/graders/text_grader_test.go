package graders

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestTextGrader_Basic(t *testing.T) {
	g, err := NewTextGrader(TextGraderArgs{
		Name:       "test",
		RegexMatch: []string{`he.*`, `world`},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindText, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestTextGrader_RegexMatch(t *testing.T) {
	tests := []struct {
		name             string
		regexMatch       []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackEq   string
		wantFeedbackPart string
	}{
		{
			name:           "all regex_match patterns match",
			regexMatch:     []string{`he.*`, `world`},
			output:         "hello world",
			wantPassed:     true,
			wantScore:      1.0,
			wantFeedbackEq: "All text checks passed",
		},
		{
			name:             "regex_match pattern missing",
			regexMatch:       []string{`hello`, `missing`},
			output:           "hello world",
			wantPassed:       false,
			wantScore:        0.5,
			wantFeedbackPart: "Missing expected pattern: missing",
		},
		{
			name:             "invalid regex_match reports failure",
			regexMatch:       []string{`[invalid`},
			output:           "anything",
			wantPassed:       false,
			wantFeedbackPart: "Invalid regex_match pattern \"[invalid\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:       "test",
				RegexMatch: tt.regexMatch,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			if tt.wantScore != 0 || tt.wantPassed {
				require.Equal(t, tt.wantScore, results.Score)
			}
			if tt.wantFeedbackEq != "" {
				require.Equal(t, tt.wantFeedbackEq, results.Feedback)
			}
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_RegexNotMatch(t *testing.T) {
	tests := []struct {
		name             string
		regexNotMatch    []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackPart string
	}{
		{
			name:          "passes when pattern absent",
			regexNotMatch: []string{`err.*`, `fail`},
			output:        "all good here",
			wantPassed:    true,
			wantScore:     1.0,
		},
		{
			name:             "fails when forbidden pattern found",
			regexNotMatch:    []string{`error`, `warning`},
			output:           "found an error in output",
			wantPassed:       false,
			wantScore:        0.5,
			wantFeedbackPart: "Found forbidden pattern: error",
		},
		{
			name:             "invalid regex_not_match reports failure",
			regexNotMatch:    []string{`[invalid`},
			output:           "anything",
			wantPassed:       false,
			wantFeedbackPart: "Invalid regex_not_match pattern \"[invalid\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:          "test",
				RegexNotMatch: tt.regexNotMatch,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			if tt.wantScore != 0 || tt.wantPassed {
				require.Equal(t, tt.wantScore, results.Score)
			}
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_Contains(t *testing.T) {
	tests := []struct {
		name             string
		contains         []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackPart string
	}{
		{
			name:       "case-insensitive match passes",
			contains:   []string{"hello", "WORLD"},
			output:     "Hello World",
			wantPassed: true,
			wantScore:  1.0,
		},
		{
			name:             "case-insensitive match fails when missing",
			contains:         []string{"hello", "missing"},
			output:           "Hello World",
			wantPassed:       false,
			wantScore:        0.5,
			wantFeedbackPart: "Missing expected substring: missing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:     "test",
				Contains: tt.contains,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			require.Equal(t, tt.wantScore, results.Score)
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_NotContains(t *testing.T) {
	tests := []struct {
		name             string
		notContains      []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackPart string
	}{
		{
			name:        "passes when forbidden substring absent",
			notContains: []string{"error", "fail"},
			output:      "all good here",
			wantPassed:  true,
			wantScore:   1.0,
		},
		{
			name:             "fails case-insensitive when forbidden substring found",
			notContains:      []string{"ERROR"},
			output:           "found an error here",
			wantPassed:       false,
			wantScore:        0.0,
			wantFeedbackPart: "Found forbidden substring: ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:        "test",
				NotContains: tt.notContains,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			require.Equal(t, tt.wantScore, results.Score)
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_ContainsCS(t *testing.T) {
	tests := []struct {
		name             string
		containsCS       []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackPart string
	}{
		{
			name:       "case-sensitive match passes",
			containsCS: []string{"Hello", "World"},
			output:     "Hello World",
			wantPassed: true,
			wantScore:  1.0,
		},
		{
			name:             "case-sensitive match fails on wrong case",
			containsCS:       []string{"hello"},
			output:           "Hello World",
			wantPassed:       false,
			wantScore:        0.0,
			wantFeedbackPart: "Missing expected substring (case-sensitive): hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:       "test",
				ContainsCS: tt.containsCS,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			require.Equal(t, tt.wantScore, results.Score)
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_NotContainsCS(t *testing.T) {
	tests := []struct {
		name             string
		notContainsCS    []string
		output           string
		wantPassed       bool
		wantScore        float64
		wantFeedbackPart string
	}{
		{
			name:          "case-sensitive not-contains passes when case differs",
			notContainsCS: []string{"ERROR"},
			output:        "found an error here",
			wantPassed:    true,
			wantScore:     1.0,
		},
		{
			name:             "case-sensitive not-contains fails on exact match",
			notContainsCS:    []string{"ERROR"},
			output:           "got an ERROR here",
			wantPassed:       false,
			wantScore:        0.0,
			wantFeedbackPart: "Found forbidden substring (case-sensitive): ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewTextGrader(TextGraderArgs{
				Name:          "test",
				NotContainsCS: tt.notContainsCS,
			})
			require.NoError(t, err)

			results, err := g.Grade(context.Background(), &Context{Output: tt.output})
			require.NoError(t, err)
			require.Equal(t, tt.wantPassed, results.Passed)
			require.Equal(t, tt.wantScore, results.Score)
			if tt.wantFeedbackPart != "" {
				require.Contains(t, results.Feedback, tt.wantFeedbackPart)
			}
		})
	}
}

func TestTextGrader_Combined(t *testing.T) {
	t.Run("all six fields pass together", func(t *testing.T) {
		g, err := NewTextGrader(TextGraderArgs{
			Name:          "test",
			Contains:      []string{"hello"},
			NotContains:   []string{"error"},
			ContainsCS:    []string{"Hello"},
			NotContainsCS: []string{"ERROR"},
			RegexMatch:    []string{`He.*ld`},
			RegexNotMatch: []string{`panic`},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "Hello World",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "All text checks passed", results.Feedback)
	})

	t.Run("mixed failures across field types", func(t *testing.T) {
		g, err := NewTextGrader(TextGraderArgs{
			Name:          "test",
			Contains:      []string{"hello"},   // pass
			NotContains:   []string{"world"},   // fail (world is present)
			ContainsCS:    []string{"Missing"}, // fail
			NotContainsCS: []string{"xyz"},     // pass
			RegexMatch:    []string{`func`},    // pass
			RegexNotMatch: []string{`panic`},   // fail (panic present)
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world func panic()",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// 3 of 6 checks fail
		require.Equal(t, 0.5, results.Score)
	})
}

func TestTextGrader_EdgeCases(t *testing.T) {
	t.Run("no fields yields score 1 and passes", func(t *testing.T) {
		g, err := NewTextGrader(TextGraderArgs{Name: "test"})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "anything",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("empty output fails contains", func(t *testing.T) {
		g, err := NewTextGrader(TextGraderArgs{
			Name:     "test",
			Contains: []string{"something"},
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
		g, err := NewTextGrader(TextGraderArgs{
			Name:       "detail-test",
			Contains:   []string{"a"},
			RegexMatch: []string{`b`},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "abc",
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, models.GraderKindText, results.Type)
		require.Equal(t, []string{"a"}, results.Details["contains"])
		require.Equal(t, []string{"b"}, results.Details["regex_match"])
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewTextGrader(TextGraderArgs{
			Name:     "test",
			Contains: []string{"ok"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "ok",
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

func TestTextGrader_ViaCreate(t *testing.T) {
	t.Run("Create with GraderKindText works", func(t *testing.T) {
		g, err := Create(models.GraderKindText, "from-create", map[string]any{
			"contains":        []string{"hello"},
			"regex_match":     []string{`world`},
			"regex_not_match": []string{`bye`},
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindText, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure TextGrader satisfies the Grader interface at compile time.
var _ Grader = (*TextGrader)(nil)
