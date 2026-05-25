package graders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

const AllPromptsPassed = "All prompts passed"
const wazaPassToolName = "set_waza_grade_pass"
const wazaFailToolName = "set_waza_grade_fail"
const promptGraderTimeout = 120 * time.Second

type promptGrader struct {
	args models.PromptGraderParameters
	name string
}

func NewPromptGrader(name string, args models.PromptGraderParameters) (*promptGrader, error) {
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

	if p.args.Mode == models.PromptGraderModePairwise && gradingContext.BaselineOutput != "" {
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
		wazaTools := newWazaGraderTools()

		if gradingContext == nil {
			return nil, errors.New("prompt grader requires grading context")
		}
		resumeID := ""
		if p.args.ContinueSession {
			if gradingContext.SessionID == "" {
				return nil, errors.New("no session id set, can't continue session in prompt grading")
			}
			resumeID = gradingContext.SessionID
		}
		resp, err := executePromptGrader(ctx, gradingContext, &execution.ExecutionRequest{
			ModelID:              p.args.Model,
			Message:              p.args.Prompt,
			Tools:                wazaTools.Tools,
			MessageMode:          execution.MessageModeEnqueue,
			Streaming:            true,
			SessionID:            resumeID,
			WorkspaceDir:         gradingContext.WorkspaceDir,
			NoSkills:             true,
			EphemeralSession:     true,
			SkipWorkspaceCapture: true,
		})

		// The SDK unconditionally sends tool results back to the model after
		// the grade tool calls fire, which starts a follow-up assistant turn.
		// That follow-up turn can fail ("Failed to get response from the AI
		// model") even though the grades were already collected. If we have
		// grade data, use it — the error is from an unnecessary follow-up
		// turn, not from the grading itself.
		if err != nil || (resp != nil && resp.ErrorMsg != "") {
			total := len(wazaTools.Failures) + len(wazaTools.Passes)
			if total == 0 {
				if err != nil {
					return nil, fmt.Errorf("failed to send prompt: %w", err)
				}
				return nil, fmt.Errorf("failed to send prompt: %s", resp.ErrorMsg)
			}
			slog.WarnContext(ctx, "prompt grader: ignoring post-grade session error (grades already collected)",
				"err", promptGraderErrorMessage(resp, err), "passes", len(wazaTools.Passes), "failures", len(wazaTools.Failures))
		}

		var score = 0.0
		total := len(wazaTools.Failures) + len(wazaTools.Passes)

		if total > 0 {
			// Can happen if they possibly messed up (we didn't get any failures or successes)
			// We'll fail the test, and avoid a divide by zero.
			score = float64(len(wazaTools.Passes)) / float64(total)
		}

		respContent := "<no response content>"
		if resp != nil && strings.TrimSpace(resp.FinalOutput) != "" {
			respContent = resp.FinalOutput
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
				"response": respContent,
				"prompt":   p.args.Prompt,
				"passes":   strings.Join(wazaTools.Passes, ";"),
				"failures": strings.Join(wazaTools.Failures, ";"),
			},
		}, nil
	})
}

func executePromptGrader(ctx context.Context, gradingContext *Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if gradingContext == nil {
		return nil, errors.New("prompt grader requires grading context")
	}
	if gradingContext.Executor == nil {
		return nil, errors.New("prompt grader requires an execution engine")
	}
	execCtx, cancel := context.WithTimeout(ctx, promptGraderTimeout)
	defer cancel()
	return gradingContext.Executor.Execute(execCtx, req)
}

func promptGraderErrorMessage(resp *execution.ExecutionResponse, err error) string {
	if err != nil {
		return err.Error()
	}
	if resp != nil {
		return resp.ErrorMsg
	}
	return ""
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
	set       bool
}

const pairwisePickToolName = "set_pairwise_winner"

func (p *promptGrader) runPairwiseOnce(
	ctx context.Context,
	gradingContext *Context,
	outputA, outputB string,
	labelA, labelB string,
) (*pairwiseJudgment, error) {
	if gradingContext == nil {
		return nil, errors.New("prompt grader requires grading context")
	}

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
				judgment.set = true
				return copilot.ToolResult{}, nil
			},
		},
	}

	prompt := buildPairwisePrompt(p.args.Prompt, outputA, outputB, labelA, labelB)
	resp, err := executePromptGrader(ctx, gradingContext, &execution.ExecutionRequest{
		ModelID:              p.args.Model,
		Message:              prompt,
		Tools:                tools,
		MessageMode:          execution.MessageModeEnqueue,
		Streaming:            true,
		WorkspaceDir:         gradingContext.WorkspaceDir,
		NoSkills:             true,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	})
	if err != nil || (resp != nil && resp.ErrorMsg != "") {
		if judgment.set {
			slog.WarnContext(ctx, "pairwise prompt grader: ignoring post-grade session error (judgment already collected)",
				"err", promptGraderErrorMessage(resp, err))
			return judgment, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to send pairwise prompt: %w", err)
		}
		return nil, fmt.Errorf("failed to send pairwise prompt: %s", resp.ErrorMsg)
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
