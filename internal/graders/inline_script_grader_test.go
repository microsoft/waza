package graders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func skipIfNoPython(t *testing.T) {
	pythonCheck := exec.Command("python", "--version")

	if err := pythonCheck.Run(); err != nil {
		t.Skip("Skipping InlineScriptGrader that needs Python")
	}
}

func skipIfNoJavascript(t *testing.T) {
	nodeCheck := exec.Command("node", "--version")

	if err := nodeCheck.Run(); err != nil {
		t.Skip("Skipping InlineScriptGrader that needs Javascript")
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
			Type:     models.GraderKindInlineScript,
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
		require.Equal(t, models.GraderKindInlineScript, grader.Kind())

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)

		// the duration is variable, so we'll test it here to make the assert
		// below a bit easier.
		require.Greater(t, results.DurationMs, int64(0))
		results.DurationMs = 0

		require.Equal(t, &models.GraderResults{
			Name:     "test",
			Type:     models.GraderKindInlineScript,
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
			Type:     models.GraderKindInlineScript,
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
			Type:     models.GraderKindInlineScript,
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
		Type:     models.GraderKindInlineScript,
		Score:    1.0,
		Passed:   true,
		Feedback: "No assertions configured",
	}, results)
}

func TestWithRealContextPython(t *testing.T) {
	skipIfNoPython(t)

	sessionEvents := loadSampleEvents(t)
	transcript := convertToTranscriptEvents(sessionEvents)

	grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
		fmt.Sprintf("len(transcript) == %d", len(sessionEvents)),
		"len(errors) == 0",
		"len(tool_calls) == 1",
		"outcome['hello'] == 'world'",
		"output == 'hello world'",
		"duration_ms < 102 and duration_ms > 100",
		"len(tool_calls[0]['result']['content']) > 0",
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Outcome: map[string]any{
			"hello": "world",
		},
		Output:     "hello world",
		Transcript: transcript,
		DurationMS: 101,
	})
	require.NoError(t, err)
	require.Equal(t, allAssertionsPassedMsg, results.Feedback)
	require.True(t, results.Passed)
}

func TestWithRealContextJavascript(t *testing.T) {
	skipIfNoJavascript(t)

	sessionEvents := loadSampleEvents(t)
	transcript := convertToTranscriptEvents(sessionEvents)

	grader, err := NewInlineScriptGrader("test", LanguageJavascript, []string{
		fmt.Sprintf("transcript.length === %d", len(sessionEvents)),
		"errors.length === 0",
		"tool_calls.length === 1",
		"outcome['hello'] === 'world'",
		"output === 'hello world'",
		"duration_ms < 102 && duration_ms > 100",
		"tool_calls[0].result.content.length > 0",
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Outcome: map[string]any{
			"hello": "world",
		},
		Output:     "hello world",
		Transcript: transcript,
		DurationMS: 101,
	})
	require.NoError(t, err)
	require.Equal(t, allAssertionsPassedMsg, results.Feedback)
	require.True(t, results.Passed)
}

func TestWithError(t *testing.T) {
	skipIfNoPython(t)

	collector := execution.NewSessionEventsCollector()

	collector.On(copilot.SessionEvent{
		Data: copilot.Data{
			Content: utils.Ptr("oh no there was a fake error"),
		},
		ID:   "2450ebe2-8dea-4cf8-9c3b-191027e4002e",
		Type: copilot.AssistantMessage,
	})

	transcript := convertToTranscriptEvents(collector.SessionEvents())

	grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
		"len(transcript) == 1",
		"len(errors) == 1", // ie, we expect some errors.
		"len(tool_calls) == 0",
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Transcript: transcript,
	})
	require.NoError(t, err)
	require.True(t, results.Passed)
}

func TestWithSyntaxError(t *testing.T) {
	t.Run("python", func(t *testing.T) {
		skipIfNoPython(t)

		grader, err := NewInlineScriptGrader("test", LanguagePython, []string{
			"what language is this, anyways?",
		})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)
		require.Contains(t, results.Feedback, "invalid syntax")
		require.False(t, results.Passed)
	})

	t.Run("javascript", func(t *testing.T) {
		skipIfNoJavascript(t)

		grader, err := NewInlineScriptGrader("test", LanguageJavascript, []string{
			"what language is this, anyways?",
		})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)
		require.Contains(t, results.Feedback, "Unexpected identifier")
		require.False(t, results.Passed)
	})
}

func TestUnsupportedLanguage(t *testing.T) {
	_, err := NewInlineScriptGrader("test", Language("ruby"), []string{"true"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "language 'ruby' is not yet supported")
}

func loadSampleEvents(t *testing.T) []copilot.SessionEvent {
	reader, err := os.Open("../testdata/copilot_events_using_skill.json")
	require.NoError(t, err)

	defer reader.Close() //nolint:errcheck

	decoder := json.NewDecoder(reader)

	sessionEventCollector := execution.NewSessionEventsCollector()

	i := -1
	for {
		i++
		var sessionEvent *copilot.SessionEvent
		err := decoder.Decode(&sessionEvent)

		if errors.Is(err, io.EOF) {
			break
		}

		require.NoErrorf(t, err, "error on iteration %d", i)
		sessionEventCollector.On(*sessionEvent)
	}

	return sessionEventCollector.SessionEvents()
}

func convertToTranscriptEvents(sessionEvents []copilot.SessionEvent) []models.TranscriptEvent {
	var transcript []models.TranscriptEvent

	for _, evt := range sessionEvents {
		transcript = append(transcript, models.TranscriptEvent{SessionEvent: evt})
	}

	return transcript
}
