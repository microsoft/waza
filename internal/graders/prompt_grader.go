package graders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

const AllPromptsPassed = "All prompts passed"
const wazaPassToolName = "set_waza_grade_pass"
const wazaFailToolName = "set_waza_grade_fail"

type PromptGraderArgs struct {
	Prompt          string `mapstructure:"prompt"`
	Model           string `mapstructure:"model"`
	ContinueSession bool   `mapstructure:"continue_session"`
	Mode            string `mapstructure:"mode"` // "independent" (default) or "pairwise"
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
	if p.args.Mode == "pairwise" && gradingContext.BaselineOutput != "" {
		return p.gradePairwise(ctx, gradingContext)
	}
	return p.gradeIndependent(ctx, gradingContext)
}

// Kind implements [Grader].
func (p *promptGrader) Kind() models.GraderKind {
	return models.GraderKindPrompt
}

// Name implements [Grader].
func (p *promptGrader) Name() string {
	return p.name
}

// gradeIndependent runs the standard single-output prompt grading.
func (p *promptGrader) gradeIndependent(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
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
					OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
					Model:               p.args.Model,
					Streaming:           true,
					Tools:               wazaTools.Tools,
				})
		} else {
			session, err = client.CreateSession(ctx, &copilot.SessionConfig{
				OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
				Model:               p.args.Model,
				Streaming:           true,
				Tools:               wazaTools.Tools,
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

// gradePairwise runs the prompt grader in pairwise comparison mode.
// It presents both outputs (baseline and skill) to the LLM judge, then
// swaps positions and re-judges to detect position bias.
func (p *promptGrader) gradePairwise(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		// Run comparison twice with swapped positions
		resultAB, err := p.runPairwiseOnce(ctx, gradingContext, gradingContext.BaselineOutput, gradingContext.Output, "A", "B")
		if err != nil {
			return nil, fmt.Errorf("pairwise pass 1 (A=baseline, B=skill) failed: %w", err)
		}

		resultBA, err := p.runPairwiseOnce(ctx, gradingContext, gradingContext.Output, gradingContext.BaselineOutput, "A", "B")
		if err != nil {
			return nil, fmt.Errorf("pairwise pass 2 (A=skill, B=baseline) failed: %w", err)
		}

		// Normalize winners to canonical labels
		winnerAB := normalizePairwiseWinner(resultAB.winner, "A", "B", "baseline", "skill")
		winnerBA := normalizePairwiseWinner(resultBA.winner, "A", "B", "skill", "baseline")

		positionConsistent := winnerAB == winnerBA

		finalWinner := winnerAB
		finalMagnitude := resultAB.magnitude
		finalReasoning := resultAB.reasoning
		if !positionConsistent {
			finalWinner = "tie"
			finalMagnitude = "equal"
			finalReasoning = fmt.Sprintf("Position-inconsistent: pass1=%s, pass2=%s. Defaulting to tie.", winnerAB, winnerBA)
		}

		score := pairwiseWinnerToScore(finalWinner)
		passed := finalWinner == "skill" || finalWinner == "tie"

		pairwise := &models.PairwiseResult{
			Winner:             finalWinner,
			Magnitude:          finalMagnitude,
			Reasoning:          finalReasoning,
			PositionConsistent: positionConsistent,
		}

		return &models.GraderResults{
			Name:   p.name,
			Type:   p.Kind(),
			Passed: passed,
			Score:  score,
			Feedback: fmt.Sprintf("pairwise: winner=%s, magnitude=%s, consistent=%v",
				finalWinner, finalMagnitude, positionConsistent),
			Details: map[string]any{
				"pairwise": pairwise,
				"pass1":    resultAB,
				"pass2":    resultBA,
				"prompt":   p.args.Prompt,
				"mode":     "pairwise",
			},
		}, nil
	})
}

type pairwiseJudgment struct {
	winner    string // "A", "B", or "tie"
	magnitude string
	reasoning string
}

const pairwisePickToolName = "set_pairwise_winner"

func (p *promptGrader) runPairwiseOnce(
	ctx context.Context,
	gradingContext *Context,
	outputA, outputB string,
	labelA, labelB string,
) (*pairwiseJudgment, error) {
	client := copilot.NewClient(&copilot.ClientOptions{
		Cwd:             gradingContext.WorkspaceDir,
		AutoStart:       utils.Ptr(true),
		AutoRestart:     utils.Ptr(true),
		UseLoggedInUser: utils.Ptr(true),
		LogLevel:        "error",
	})

	defer func() {
		if err := client.Stop(); err != nil {
			slog.ErrorContext(ctx, "error stopping client for pairwise grader")
		}
	}()

	judgment := &pairwiseJudgment{
		winner:    "tie",
		magnitude: "equal",
	}

	tools := []copilot.Tool{
		{
			Name:        pairwisePickToolName,
			Description: "Report the winner of the pairwise comparison.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"winner": map[string]any{
						"type":        "string",
						"enum":        []string{labelA, labelB, "tie"},
						"description": fmt.Sprintf("Which output is better: %s, %s, or tie", labelA, labelB),
					},
					"magnitude": map[string]any{
						"type":        "string",
						"enum":        []string{"much-better", "slightly-better", "equal"},
						"description": "How much better the winner is",
					},
					"reasoning": map[string]any{
						"type":        "string",
						"description": "Brief explanation of why this output won",
					},
				},
				"required": []string{"winner", "magnitude", "reasoning"},
			},
			Handler: func(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args struct {
					Winner    string `mapstructure:"winner"`
					Magnitude string `mapstructure:"magnitude"`
					Reasoning string `mapstructure:"reasoning"`
				}
				if err := mapstructure.Decode(invocation.Arguments, &args); err != nil {
					return copilot.ToolResult{}, nil
				}
				judgment.winner = args.Winner
				judgment.magnitude = args.Magnitude
				judgment.reasoning = args.Reasoning
				return copilot.ToolResult{}, nil
			},
		},
	}

	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Model:               p.args.Model,
		Streaming:           true,
		Tools:               tools,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create session for pairwise grading: %w", err)
	}

	session.On(utils.SessionToSlog)

	prompt := buildPairwisePrompt(p.args.Prompt, outputA, outputB, labelA, labelB)

	_, err = session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: prompt,
		Mode:   "enqueue",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send pairwise prompt: %w", err)
	}

	return judgment, nil
}

func buildPairwisePrompt(rubric, outputA, outputB, labelA, labelB string) string {
	var sb strings.Builder
	sb.WriteString("You are a judge comparing two outputs for the same task.\n\n")
	sb.WriteString("## Rubric\n")
	sb.WriteString(rubric)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "## Output %s\n```\n%s\n```\n\n", labelA, outputA)
	fmt.Fprintf(&sb, "## Output %s\n```\n%s\n```\n\n", labelB, outputB)
	fmt.Fprintf(&sb, "Compare both outputs against the rubric. Call set_pairwise_winner with your verdict: \"%s\", \"%s\", or \"tie\".\n", labelA, labelB)
	return sb.String()
}

// normalizePairwiseWinner maps positional labels (A/B) to semantic labels (baseline/skill).
func normalizePairwiseWinner(winner, labelA, labelB, semanticA, semanticB string) string {
	switch winner {
	case labelA:
		return semanticA
	case labelB:
		return semanticB
	default:
		return "tie"
	}
}

func pairwiseWinnerToScore(winner string) float64 {
	switch winner {
	case "skill":
		return 1.0
	case "tie":
		return 0.5
	default: // "baseline"
		return 0.0
	}
}
