package graders

import (
	"context"
	"os/exec"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func skipIfNoPython(t *testing.T) {
	pythonCheck := exec.Command("python", "--version")

	if err := pythonCheck.Run(); err != nil {
		t.Skip("Skipping InlineScriptGrader that needs Python")
	}
}

func TestInlineScriptGrader(t *testing.T) {
	skipIfNoPython(t)

	t.Run("basic_success", func(t *testing.T) {
		grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
			"1 == 1",
		})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)

		// the duration is variable, so we'll test it here to make the assert
		// below a bit easier.
		require.Greater(t, results.DurationMs, int64(0))
		results.DurationMs = 0

		require.Equal(t, &models.GraderResults{
			Name:     "test",
			Type:     string(TypeInlineScript),
			Score:    1.0,
			Passed:   true,
			Feedback: "All assertions passed",
			Details: map[string]any{
				"total_assertions":  1,
				"passed_assertions": 1,
				"failures":          []string(nil),
			},
			DurationMs: 0,
		}, results)
	})

	t.Run("basic_failure", func(t *testing.T) {
		grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
			"1 == 0",
		})
		require.NoError(t, err)
		require.Equal(t, "test", grader.Name())
		require.Equal(t, TypeInlineScript, grader.Type())

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)

		// the duration is variable, so we'll test it here to make the assert
		// below a bit easier.
		require.Greater(t, results.DurationMs, int64(0))
		results.DurationMs = 0

		require.Equal(t, &models.GraderResults{
			Name:     "test",
			Type:     string(TypeInlineScript),
			Score:    0.0,
			Passed:   false,
			Feedback: "Failed: 1 == 0",
			Details: map[string]any{
				"total_assertions":  1,
				"passed_assertions": 0,
				"failures":          []string{"Failed: 1 == 0"},
			},
			DurationMs: 0,
		}, results)
	})

	t.Run("partial_pass_fail", func(t *testing.T) {
		grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
			"1 == 1",
			"2 == 3",
			"3 == 3",
			"4 == 5",
		})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)

		require.Greater(t, results.DurationMs, int64(0))
		results.DurationMs = 0

		require.Equal(t, &models.GraderResults{
			Name:     "test",
			Type:     string(TypeInlineScript),
			Score:    0.5, // 2 of 4 passed
			Passed:   false,
			Feedback: "Failed: 2 == 3; Failed: 4 == 5",
			Details: map[string]any{
				"total_assertions":  4,
				"passed_assertions": 2,
				"failures":          []string{"Failed: 2 == 3", "Failed: 4 == 5"},
			},
			DurationMs: 0,
		}, results)
	})

	t.Run("with_context_output", func(t *testing.T) {
		grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
			`"hello" in output`,
			`len(output) > 0`,
		})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{
			Output: "hello world",
		})
		require.NoError(t, err)

		require.Greater(t, results.DurationMs, int64(0))
		results.DurationMs = 0

		require.Equal(t, &models.GraderResults{
			Name:     "test",
			Type:     string(TypeInlineScript),
			Score:    1.0,
			Passed:   true,
			Feedback: "All assertions passed",
			Details: map[string]any{
				"total_assertions":  2,
				"passed_assertions": 2,
				"failures":          []string(nil),
			},
			DurationMs: 0,
		}, results)
	})
}

func TestEmptyAssertions(t *testing.T) {
	grader, err := NewInlineScriptGrader("test", LanguagePython, []string{})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{})
	require.NoError(t, err)

	require.Equal(t, &models.GraderResults{
		Name:     "test",
		Type:     string(TypeInlineScript),
		Score:    1.0,
		Passed:   true,
		Feedback: "No assertions configured",
	}, results)
}

func TestUnsupportedLanguage(t *testing.T) {
	_, err := NewInlineScriptGrader("test", Language("ruby"), []string{"true"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "language 'ruby' is not yet supported")
}
