package graders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/utils"
)

const AllPromptsPassed = "All prompts passed"
const wazaPassToolName = "set_waza_grade_pass"
const wazaFailToolName = "set_waza_grade_fail"

type PromptGraderArgs struct {
	Prompt          string `mapstructure:"prompt"`
	Model           string `mapstructure:"model"`
	ContinueSession bool   `mapstructure:"continue_session"`
}

type promptGrader struct {
	args PromptGraderArgs
	name string
}

func NewPromptGrader(name string, args PromptGraderArgs) (*promptGrader, error) {
	if name == "" {
		return nil, errors.New("missing name")
	}

	if args.Prompt == "" {
		return nil, errors.New("required field 'prompt' is missing")
	}

	return &promptGrader{
		name: name,
		args: args,
	}, nil
}

// Grade implements [Grader].
func (p *promptGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		client := copilot.NewClient(&copilot.ClientOptions{
			Cwd:             gradingContext.WorkspaceDir,
			AutoStart:       utils.Ptr(true),
			AutoRestart:     utils.Ptr(true),
			UseLoggedInUser: utils.Ptr(true),
			LogLevel:        "error",
		})

		defer func() {
			if err := client.Stop(); err != nil {
				slog.ErrorContext(ctx, "error stopping client for prompt grader")
			}
		}()

		var session *copilot.Session
		var err error
		wazaTools := newWazaGraderTools()

		if p.args.ContinueSession {
			if gradingContext.SessionID == "" {
				return nil, errors.New("no session id set, can't continue session in prmopt grading")
			}

			// resume the previous session, but use a different model for the judge.
			session, err = client.ResumeSessionWithOptions(ctx,
				gradingContext.SessionID,
				&copilot.ResumeSessionConfig{
					Model:     p.args.Model,
					Streaming: true,
					Tools:     wazaTools.Tools,
				})
		} else {
			session, err = client.CreateSession(ctx, &copilot.SessionConfig{
				Model:     p.args.Model,
				Streaming: true,
				Tools:     wazaTools.Tools,
			})
		}

		if err != nil {
			return nil, fmt.Errorf("failed to start up copilot session for prompt grading: %w", err)
		}

		session.On(utils.SessionToSlog)

		resp, err := session.SendAndWait(ctx, copilot.MessageOptions{
			Prompt: p.args.Prompt,
			Mode:   "enqueue",
		})

		if err != nil {
			return nil, fmt.Errorf("failed to send prompt: %w", err)
		}

		var score = 0.0
		total := len(wazaTools.Failures) + len(wazaTools.Passes)

		if total > 0 {
			// Can happen if they possibly messed up (we didn't get any failures or successes)
			// We'll fail the test, and avoid a divide by zero.
			score = float64(len(wazaTools.Passes)) / float64(total)
		}

		respContent := resp.Data.Content

		if respContent == nil {
			respContent = utils.Ptr("<no response content>")
		}

		feedback := AllPromptsPassed

		if len(wazaTools.Failures) > 0 {
			feedback = strings.Join(wazaTools.Failures, ";")
		}

		return &models.GraderResults{
			Name:     p.name,
			Type:     p.Kind(),
			Passed:   len(wazaTools.Failures) == 0 && len(wazaTools.Passes) > 0,
			Score:    score,
			Feedback: feedback,
			Details: map[string]any{
				"response": *respContent,
				"prompt":   p.args.Prompt,
				"passes":   strings.Join(wazaTools.Passes, ";"),
				"failures": strings.Join(wazaTools.Failures, ";"),
			},
		}, nil
	})
}

// Kind implements [Grader].
func (p *promptGrader) Kind() models.GraderKind {
	return models.GraderKindPrompt
}

// Name implements [Grader].
func (p *promptGrader) Name() string {
	return p.name
}

func newWazaGraderTools() *struct {
	Tools    []copilot.Tool
	Passes   []string
	Failures []string
} {
	r := &struct {
		Tools    []copilot.Tool
		Passes   []string
		Failures []string
	}{}

	r.Tools = []copilot.Tool{
		{
			Name:        wazaPassToolName,
			Description: "Used by waza graders, this marks the check as passed. This can be called multiple times.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{
						"type":        "string",
						"description": "Optional description of the passing check",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional reason for the passing check",
					},
				},
			},
			Handler: func(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args *struct {
					Description string `mapstructure:"description"`
					Reason      string `mapstructure:"reason"`
				}

				var pass string

				if err := mapstructure.Decode(invocation.Arguments, &args); err != nil {
					pass = "pass" // can't extract an argument, shouldn't cause a test to fail.
				} else {
					pass = fmt.Sprintf("pass: %s: %s", args.Description, args.Reason)
				}

				r.Passes = append(r.Passes, pass)
				return copilot.ToolResult{}, nil
			},
		},
		{
			Name:        wazaFailToolName,
			Description: "Used by waza graders, this marks the check as failed, with an optional reason. This can be called multiple times.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{
						"type":        "string",
						"description": "Optional description of the failing check",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional reason for the failing check",
					},
				},
			},
			Handler: func(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args *struct {
					Description string `mapstructure:"description"`
					Reason      string `mapstructure:"reason"`
				}

				var failure string

				if err := mapstructure.Decode(invocation.Arguments, &args); err != nil {
					failure = "fail"
				} else {
					failure = fmt.Sprintf("fail: %s: %s", args.Description, args.Reason)
				}

				r.Failures = append(r.Failures, failure)
				return copilot.ToolResult{}, nil
			},
		},
	}

	return r
}
