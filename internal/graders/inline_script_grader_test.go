package graders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func skipIfNoPython(t *testing.T) {
	pythonCheck := exec.Command(resolvePythonBin(), "--version")

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

func TestInlineScriptRecoveryEvidence(t *testing.T) {
	for _, language := range []models.Language{models.LanguagePython, models.LanguageJavascript} {
		t.Run(string(language), func(t *testing.T) {
			if language == models.LanguagePython {
				skipIfNoPython(t)
			} else {
				skipIfNoJavascript(t)
			}
			for _, present := range []bool{false, true} {
				t.Run(fmt.Sprint(present), func(t *testing.T) {
					ctx := &Context{}
					assertions := []string{"len(tool_events) == 0", "len(command_invocations) == 0"}
					if language == models.LanguageJavascript {
						assertions = []string{"tool_events.length == 0", "command_invocations.length == 0"}
					}
					if present {
						ctx.ToolEvents = []models.ToolEvent{{
							ToolName: "dependency-request", Args: map[string]any{"operation": "transient"},
							Success: false, Error: "temporary failure",
						}}
						ctx.CommandInvocations = []models.CommandInvocation{
							{Command: "az", ExitCode: new(0)},
							{Command: "az"},
						}
						assertions = []string{
							"tool_events[0]['args']['operation'] == 'transient'",
							"tool_events[0]['error'] == 'temporary failure'",
							"len(command_invocations) == 2",
							"command_invocations[0]['exit_code'] == 0",
						}
						if language == models.LanguagePython {
							assertions = append(assertions, "'exit_code' not in command_invocations[1]")
						} else {
							assertions[2] = "command_invocations.length == 2"
							assertions = append(assertions, "!('exit_code' in command_invocations[1])")
						}
					}
					grader, err := Create("evidence", models.InlineScriptGraderParameters{Language: language, Assertions: assertions})
					require.NoError(t, err)
					result, err := grader.Grade(t.Context(), ctx)
					require.NoError(t, err)
					require.True(t, result.Passed, "%+v", result)
				})
			}
		})
	}
}

func TestInlineScriptNativeUserInputEvidence(t *testing.T) {
	for _, language := range []models.Language{models.LanguagePython, models.LanguageJavascript} {
		t.Run(string(language), func(t *testing.T) {
			if language == models.LanguagePython {
				skipIfNoPython(t)
			} else {
				skipIfNoJavascript(t)
			}
			answer := "staging"
			ctx := &Context{UserInputRequests: []models.UserInputRecord{{
				Sequence: 1, SessionID: "s-1", Question: "Deploy where?", Choices: []string{"staging", "prod"},
				Source: models.UserInputSourceScript, Answer: &answer, WasFreeform: new(false),
			}}}
			absent := []string{"len(user_input_requests) == 0"}
			present := []string{
				"len(user_input_requests) == 1",
				"user_input_requests[0]['answer'] == 'staging'",
				"user_input_requests[0]['was_freeform'] == False",
				"user_input_requests[0]['choices'] == ['staging', 'prod']",
			}
			if language == models.LanguageJavascript {
				absent = []string{"user_input_requests.length == 0"}
				present = []string{
					"user_input_requests.length == 1",
					"user_input_requests[0].answer == 'staging'",
					"user_input_requests[0].was_freeform === false",
					"user_input_requests[0].choices.join(',') == 'staging,prod'",
				}
			}
			for _, tc := range []struct {
				ctx        *Context
				assertions []string
				pass       bool
			}{{&Context{}, absent, true}, {ctx, present, true}, {&Context{}, present[:1], false}} {
				grader, err := Create("questions", models.InlineScriptGraderParameters{Language: language, Assertions: tc.assertions})
				require.NoError(t, err)
				result, err := grader.Grade(t.Context(), tc.ctx)
				require.NoError(t, err)
				require.Equal(t, tc.pass, result.Passed, "%+v", result)
			}
		})
	}
}

func TestInlineScriptGrader(t *testing.T) {
	skipIfNoPython(t)

	t.Run("basic_success", func(t *testing.T) {
		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
			"1 == 1",
		}})
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
		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
			"1 == 0",
		}})
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
		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
			"1 == 1",
			"2 == 3",
			"3 == 3",
			"4 == 5",
		}})
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
		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
			`"hello" in output`,
			`len(output) > 0`,
		}})
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
	grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{}})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{})
	require.NoError(t, err)

	require.GreaterOrEqual(t, results.DurationMs, int64(0))
	results.DurationMs = 0

	require.Equal(t, &models.GraderResults{
		Name:     "test",
		Type:     models.GraderKindInlineScript,
		Score:    1.0,
		Passed:   true,
		Feedback: "No assertions configured",
	}, results)
}

func TestInlineScriptEvaluationErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		language   models.Language
		assertions []string
		errors     bool
	}{
		{"python-false", models.LanguagePython, []string{"False"}, false},
		{"python-exception", models.LanguagePython, []string{"1 / 0"}, true},
		{"python-mixed", models.LanguagePython, []string{"True", "False", "missing_field"}, true},
		{"javascript-false", models.LanguageJavascript, []string{"false"}, false},
		{"javascript-exception", models.LanguageJavascript, []string{"missing_field"}, true},
		{"javascript-empty-exception", models.LanguageJavascript, []string{`(() => { throw ""; })()`}, true},
		{"javascript-fail-exception", models.LanguageJavascript, []string{`(() => { throw "fail"; })()`}, true},
		{"javascript-stateful-exception", models.LanguageJavascript, []string{
			`(() => { throw { count: 0, toString() { if (this.count++) throw "converted twice"; return "first"; } }; })()`,
		}, true},
		{"javascript-stateful-changing", models.LanguageJavascript, []string{
			`(() => { throw { count: 0, toString() { return this.count++ ? "second" : "first"; } }; })()`,
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.language == models.LanguagePython {
				skipIfNoPython(t)
			} else {
				skipIfNoJavascript(t)
			}
			g, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{
				Language: tc.language, Assertions: tc.assertions,
			})
			require.NoError(t, err)
			result, err := g.Grade(t.Context(), &Context{})
			require.NoError(t, err)
			require.False(t, result.Passed)
			_, hasErrors := result.Details["evaluation_errors"]
			require.Equal(t, tc.errors, hasErrors)
			if tc.errors {
				require.Len(t, result.Details["evaluation_errors"], 1)
			}
			if strings.HasPrefix(tc.name, "javascript-stateful-") {
				require.Equal(t, []string{"first"}, result.Details["evaluation_errors"])
				require.Equal(t, []string{"Failed: " + tc.assertions[0] + ": first"}, result.Details["failures"])
				require.Zero(t, result.Score)
			}
			require.Equal(t, len(tc.assertions), result.Details["total_assertions"])
		})
	}
}

func TestInlineScriptPythonExceptionStringifiesOnce(t *testing.T) {
	skipIfNoPython(t)
	for _, tc := range []struct {
		name, secondConversion string
	}{
		{"changing-message", `"second"`},
		{"throwing-conversion", "1 / 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Construct the exception using only the public assertion context.
			assertion := fmt.Sprintf(`(_ for _ in []).throw(int.__class__("StatefulError", (re.error,), {"__str__": lambda self: (self.__dict__.update(calls=self.__dict__.get("calls", 0) + 1), "first" if self.calls == 1 else %s)[1]})("unused"))`, tc.secondConversion)
			g, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{
				Language: models.LanguagePython, Assertions: []string{assertion, "True", "False"},
			})
			require.NoError(t, err)
			result, err := g.Grade(t.Context(), &Context{})
			require.NoError(t, err)
			require.False(t, result.Passed)
			require.Equal(t, float64(1)/3, result.Score)
			require.Equal(t, 3, result.Details["total_assertions"])
			require.Equal(t, 1, result.Details["passed_assertions"])
			require.Equal(t, []string{"first"}, result.Details["evaluation_errors"])
			failures := []string{"Failed: " + assertion + ": first", "Failed: False"}
			require.Equal(t, failures, result.Details["failures"])
			require.Equal(t, strings.Join(failures, "; "), result.Feedback)
		})
	}
}

func TestInlineScriptEvaluationErrorsInterpreterIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PATH isolation uses executable symlinks; language subtests still run natively")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, interpreter, passed, skipped string
	}{
		{"python-only", "python3", "python-exception", "javascript-exception"},
		{"node-only", "node", "javascript-exception", "python-exception"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := exec.LookPath(tc.interpreter)
			if err != nil {
				t.Skipf("%s unavailable: %v", tc.interpreter, err)
			}
			dir := t.TempDir()
			name := tc.interpreter
			if filepath.Ext(path) == ".exe" {
				name += ".exe"
			}
			if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestInlineScriptEvaluationErrors$", "-test.v")
			cmd.Env = append(os.Environ(), "PATH="+dir)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Contains(t, string(output), "--- PASS: TestInlineScriptEvaluationErrors/"+tc.passed)
			require.Contains(t, string(output), "--- SKIP: TestInlineScriptEvaluationErrors/"+tc.skipped)
		})
	}
}

func TestWithRealContextPython(t *testing.T) {
	skipIfNoPython(t)

	sessionEvents := loadSampleEvents(t)
	transcript := convertToTranscriptEvents(sessionEvents)

	grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
		fmt.Sprintf("len(transcript) == %d", len(sessionEvents)),
		"len(errors) == 0",
		"len(tool_calls) == 1",
		"outcome['hello'] == 'world'",
		"output == 'hello world'",
		"duration_ms < 102 and duration_ms > 100",
		"len(tool_calls[0]['result']['content']) > 0",
	}})
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

	grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguageJavascript, Assertions: []string{
		fmt.Sprintf("transcript.length === %d", len(sessionEvents)),
		"errors.length === 0",
		"tool_calls.length === 1",
		"outcome['hello'] === 'world'",
		"output === 'hello world'",
		"duration_ms < 102 && duration_ms > 100",
		"tool_calls[0].result.content.length > 0",
	}})
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
		Data: &copilot.AssistantMessageData{Content: "oh no there was a fake error"},
		ID:   "2450ebe2-8dea-4cf8-9c3b-191027e4002e",
	})

	transcript := convertToTranscriptEvents(collector.SessionEvents())

	grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
		"len(transcript) == 1",
		"len(errors) == 1", // ie, we expect some errors.
		"len(tool_calls) == 0",
	}})
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

		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguagePython, Assertions: []string{
			"what language is this, anyways?",
		}})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)
		require.Contains(t, results.Feedback, "invalid syntax")
		require.False(t, results.Passed)
	})

	t.Run("javascript", func(t *testing.T) {
		skipIfNoJavascript(t)

		grader, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.LanguageJavascript, Assertions: []string{
			"what language is this, anyways?",
		}})
		require.NoError(t, err)

		results, err := grader.Grade(context.Background(), &Context{})
		require.NoError(t, err)
		require.Contains(t, results.Feedback, "Unexpected identifier")
		require.False(t, results.Passed)
	})
}

func TestUnsupportedLanguage(t *testing.T) {
	_, err := NewInlineScriptGrader("test", models.InlineScriptGraderParameters{Language: models.Language("ruby"), Assertions: []string{"true"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "language 'ruby' is not yet supported")
}

func loadSampleEvents(t *testing.T) []copilot.SessionEvent {
	reader, err := os.Open(filepath.Join("..", "testdata", "copilot_events_using_skill.json"))
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
