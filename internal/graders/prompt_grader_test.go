package graders

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

var _ Grader = (*promptGrader)(nil)
var enableCopilotTests = os.Getenv("ENABLE_COPILOT_TESTS") == "true"

const basicModel = "gpt-4o-mini"
const advancedModel = "claude-sonnet-4.5"

func skipIfCopilotNotEnabled(t *testing.T) {
	if !enableCopilotTests {
		t.Skip("Copilot tests can be enabled by setting ENABLE_COPILOT_TESTS=true")
	}
}

func TestNewPromptGrader(t *testing.T) {
	_, err := NewPromptGrader("", models.PromptGraderParameters{
		Model: "a-model",
	})
	require.ErrorContains(t, err, "missing name")

	_, err = NewPromptGrader("name", models.PromptGraderParameters{
		Model: "",
	})
	require.ErrorContains(t, err, "required field 'prompt' is missing")
}

func TestPromptGraderNoContinueWithoutIDFails(t *testing.T) {
	promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
		Prompt:          "Prompt not used",
		ContinueSession: true,
	})
	require.NoError(t, err)

	results, err := promptGrader.Grade(context.Background(), &Context{
		SessionID: "",
	})
	require.ErrorContains(t, err, "no session id set, can't continue session in prmopt grading")
	require.Empty(t, results)
}

func TestPromptGraderLive(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	logLevel := &slog.LevelVar{}
	logLevel.Set(slog.LevelInfo)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)
	logLevel.Set(slog.LevelDebug)

	t.Run("passing_prompt", func(t *testing.T) {
		promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
			Prompt: "This test is whether math still works, or not. Check that 4+4 is 8. If it is call set_waza_grade_pass. If it's not, then call set_waza_grade_fail, with the reason that the world is no longer real.",
		})
		require.NoError(t, err)

		results, err := promptGrader.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)

		require.Equal(t, AllPromptsPassed, results.Feedback)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("failing_prompt", func(t *testing.T) {
		promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
			Prompt: "This test is whether math still works, or not. Check that 4+4 is 9. If it is call set_waza_grade_pass. If it's not, then call set_waza_grade_fail, with the reason that the world is no longer real.",
		})
		require.NoError(t, err)

		results, err := promptGrader.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)

		require.NotEmpty(t, results.Feedback)
		require.Contains(t, strings.ToLower(results.Feedback), "the world")
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})

	t.Run("pass_fail_prompt", func(t *testing.T) {
		promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
			Prompt: "This test is whether math still works, or not. Check that 4+4 is 9. If it is call set_waza_grade_pass. If it's not, then call set_waza_grade_fail, with the reason that the world is no longer real. Then, for no reason that I can think of, call set_waza_grade_pass, with a description of whimsy",
		})
		require.NoError(t, err)

		results, err := promptGrader.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)

		require.NotEmpty(t, results.Feedback)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
	})
}

func TestPromptUsingTools(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	t.Run("check_fours_pass", func(t *testing.T) {
		promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
			Model: advancedModel,
			Prompt: "Test if we have any 4s listed in our conversation:\n" +
				"If there are, call set_waza_grade_pass.\n" +
				"If there aren't, then call set_waza_grade_fail, with the reason",
		})
		require.NoError(t, err)

		results, err := promptGrader.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)

		require.Equal(t, AllPromptsPassed, results.Feedback)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("check_fours_fail", func(t *testing.T) {
		promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
			Model: advancedModel,
			// we purposefully flip this so we "fail" if there are any 4s.
			Prompt: "Test if we have any 4s listed in our conversation:\n" +
				"- If there are, then call set_waza_grade_fail, with your reasoning\n" +
				"- If there aren't, call set_waza_grade_pass, with your reasoning\n",
		})
		require.NoError(t, err)

		results, err := promptGrader.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)

		require.NotEqual(t, AllPromptsPassed, results.Feedback)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
	})
}

func TestUsingPreviousSessionID(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	var sessionID string
	var randomString string
	{
		// we're going to create a session and "store" a number in it, and then see if we can recall it in our
		// prompt evaluation below.
		client := copilot.NewClient(&copilot.ClientOptions{
			AutoStart:       utils.Ptr(true),
			UseLoggedInUser: utils.Ptr(true),
		})

		session, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
			Model:               basicModel,
			OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		})
		require.NoError(t, err)

		sessionID = session.SessionID

		numBytes := [8]byte{}
		n, err := rand.Read(numBytes[:])
		require.NoError(t, err)
		require.Equal(t, 8, n)

		randomString = hex.EncodeToString(numBytes[:])

		resp, err := session.SendAndWait(context.Background(), copilot.MessageOptions{
			Prompt: "Remember this random string: " + randomString,
		})
		require.NoError(t, err)

		t.Logf("Content: %s", *resp.Data.Content)

		resp, err = session.SendAndWait(context.Background(), copilot.MessageOptions{
			Prompt: "what was the random string?",
		})
		require.NoError(t, err)

		if resp.Data.Content != nil {
			t.Logf("Content: %s", *resp.Data.Content)
		}

		err = client.Stop()
		require.NoError(t, err)
	}

	promptGrader, err := NewPromptGrader("my-prompt-grader", models.PromptGraderParameters{
		ContinueSession: true,
		Model:           advancedModel,
		Prompt: "This is a test to see if there have been any random strings mentioned in our conversation, and if there are, what the value is.\n" +
			"- If you find it, then call set_waza_grade_pass, with the random string\n" +
			"- If you don't, call set_waza_grade_fail, with a reason you can't remember it\n",
	})
	require.NoError(t, err)

	results, err := promptGrader.Grade(context.Background(), &Context{
		WorkspaceDir: "",
		SessionID:    sessionID,
	})
	require.NoError(t, err)

	t.Logf("%#v", results)

	require.Equal(t, AllPromptsPassed, results.Feedback)
	require.True(t, results.Passed)
	require.Equal(t, 1.0, results.Score)

	// check that our random string was actually found by the chat session!
	require.Contains(t, results.Details["passes"], randomString)
}

func TestLoadPromptGrader(t *testing.T) {
	spec, err := models.LoadEvalSpec("testdata/eval-grader-prompt.yaml")
	require.NoError(t, err)

	grader, err := Create(spec.Graders[0].Identifier, spec.Graders[0].Parameters)
	require.NoError(t, err)
	require.Equal(t, &promptGrader{
		args: models.PromptGraderParameters{
			Prompt: fmt.Sprintf("Check if the responses include proper explanations for our skill.\n"+
				"If it does, call %s.\n"+
				"If it does not, then call %s, with your reasoning.", wazaPassToolName, wazaFailToolName),
			Model:           "gpt-4o-mini",
			ContinueSession: true,
		},
		name: "with-continue-session",
	}, grader)

	grader, err = Create(spec.Graders[1].Identifier, spec.Graders[1].Parameters)
	require.NoError(t, err)

	require.Equal(t, &promptGrader{
		args: models.PromptGraderParameters{
			Prompt: fmt.Sprintf("Check to see that the files on disk are properly updated, by some criteria. "+
				"If they are correct, call %s. "+
				"If it's not, then call %s, with your reasoning.", wazaPassToolName, wazaFailToolName),
			Model:           "claude-sonnet-4.5",
			ContinueSession: false,
		},
		name: "without-continue-session",
	}, grader)
}

func TestPairwiseMode_Validation(t *testing.T) {
	// Pairwise mode requires a prompt
	_, err := NewPromptGrader("pairwise-grader", models.PromptGraderParameters{
		Model: "gpt-4o",
		Mode:  "pairwise",
	})
	require.ErrorContains(t, err, "required field 'prompt' is missing")
}

func TestPairwiseMode_FallsBackToIndependent(t *testing.T) {
	// When mode is pairwise but no baseline output is available, falls back to independent
	skipIfCopilotNotEnabled(t)

	grader, err := NewPromptGrader("pairwise-grader", models.PromptGraderParameters{
		Prompt: "Check that the output mentions 'hello'. If it does, call set_waza_grade_pass. Otherwise call set_waza_grade_fail.",
		Model:  basicModel,
		Mode:   "pairwise",
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Output:         "hello world",
		BaselineOutput: "", // empty => falls back to independent
	})
	require.NoError(t, err)
	require.NotNil(t, results)
	// Should grade independently since no baseline output
	require.Equal(t, models.GraderKindPrompt, results.Type)
}

func TestPairwiseBuildPrompt(t *testing.T) {
	prompt := buildPairwisePrompt("Is the code correct?", "output A content", "output B content", "A", "B")
	require.Contains(t, prompt, "Output A")
	require.Contains(t, prompt, "Output B")
	require.Contains(t, prompt, "output A content")
	require.Contains(t, prompt, "output B content")
	require.Contains(t, prompt, "Is the code correct?")
	require.Contains(t, prompt, "set_pairwise_winner")
}

func TestNormalizePairwiseWinner(t *testing.T) {
	tests := []struct {
		winner   string
		expected string
	}{
		{"A", "baseline"},
		{"B", "skill"},
		{"tie", "tie"},
		{"unknown", "tie"},
	}

	for _, tt := range tests {
		got := normalizePairwiseWinner(tt.winner, "A", "B", "baseline", "skill")
		require.Equal(t, tt.expected, got, "winner=%s", tt.winner)
	}
}

func TestPairwiseWinnerToScore(t *testing.T) {
	require.Equal(t, 1.0, pairwiseWinnerToScore("skill"))
	require.Equal(t, 0.5, pairwiseWinnerToScore("tie"))
	require.Equal(t, 0.0, pairwiseWinnerToScore("baseline"))
}

func TestPairwiseMode_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	grader, err := NewPromptGrader("pairwise-quality", models.PromptGraderParameters{
		Prompt: "Which output better explains what a goroutine is? Pick the one that is more accurate and complete.",
		Model:  basicModel,
		Mode:   "pairwise",
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Output:         "A goroutine is a lightweight thread of execution managed by the Go runtime. They are multiplexed onto OS threads and are very cheap to create (a few KB of stack).",
		BaselineOutput: "goroutine is a thing in Go",
	})
	require.NoError(t, err)
	require.NotNil(t, results)
	require.Equal(t, "pairwise", results.Details["mode"])

	pairwise, ok := results.Details["pairwise"].(*models.PairwiseResult)
	require.True(t, ok, "pairwise detail should be *models.PairwiseResult")
	require.Contains(t, []string{"skill", "baseline", "tie"}, pairwise.Winner)
}
