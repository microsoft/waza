package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// behaviorGrader validates agent behavior metrics such as tool usage,
// token consumption, forbidden actions, and duration limits.
type behaviorGrader struct {
	name           string
	maxToolCalls   int
	maxTokens      int
	requiredTools  []string
	forbiddenTools []string
	maxDurationMS  int64
}

// BehaviorGraderParams holds the mapstructure-decoded parameters for the behavior grader.
type BehaviorGraderParams struct {
	MaxToolCalls   int      `mapstructure:"max_tool_calls"`
	MaxTokens      int      `mapstructure:"max_tokens"`
	RequiredTools  []string `mapstructure:"required_tools"`
	ForbiddenTools []string `mapstructure:"forbidden_tools"`
	MaxDurationMS  int64    `mapstructure:"max_duration_ms"`
}

// NewBehaviorGrader creates a behaviorGrader from decoded parameters.
func NewBehaviorGrader(name string, params BehaviorGraderParams) (*behaviorGrader, error) {
	if params.MaxToolCalls == 0 && params.MaxTokens == 0 &&
		len(params.RequiredTools) == 0 && len(params.ForbiddenTools) == 0 &&
		params.MaxDurationMS == 0 {
		return nil, fmt.Errorf("behavior grader '%s' must have at least one rule configured", name)
	}

	return &behaviorGrader{
		name:           name,
		maxToolCalls:   params.MaxToolCalls,
		maxTokens:      params.MaxTokens,
		requiredTools:  params.RequiredTools,
		forbiddenTools: params.ForbiddenTools,
		maxDurationMS:  params.MaxDurationMS,
	}, nil
}

func (bg *behaviorGrader) Name() string            { return bg.name }
func (bg *behaviorGrader) Kind() models.GraderKind { return models.GraderKindBehavior }

func (bg *behaviorGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		session := gradingContext.Session
		if session == nil {
			return &models.GraderResults{
				Name:     bg.name,
				Type:     models.GraderKindBehavior,
				Score:    0.0,
				Passed:   false,
				Feedback: "No session digest available for behavior grading",
			}, nil
		}

		var failures []string

		failures = append(failures, bg.checkMaxToolCalls(session)...)
		failures = append(failures, bg.checkMaxTokens(session)...)
		failures = append(failures, bg.checkRequiredTools(session)...)
		failures = append(failures, bg.checkForbiddenTools(session)...)
		failures = append(failures, bg.checkMaxDuration(gradingContext.DurationMS)...)

		totalChecks := bg.countTotalChecks()
		passedChecks := totalChecks - len(failures)

		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}

		feedback := "All behavior checks passed"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}

		details := map[string]any{
			"max_tool_calls":  bg.maxToolCalls,
			"max_tokens":      bg.maxTokens,
			"required_tools":  bg.requiredTools,
			"forbidden_tools": bg.forbiddenTools,
			"max_duration_ms": bg.maxDurationMS,
			"failures":        failures,
			"tool_call_count": session.ToolCallCount,
			"tools_used":      session.ToolsUsed,
			"actual_duration": gradingContext.DurationMS,
		}
		if session.Usage != nil {
			details["tokens_total"] = session.Usage.InputTokens + session.Usage.OutputTokens
		}
		return &models.GraderResults{
			Name:     bg.name,
			Type:     models.GraderKindBehavior,
			Score:    score,
			Passed:   len(failures) == 0,
			Feedback: feedback,
			Details:  details,
		}, nil
	})
}

// checkMaxToolCalls validates tool call count is within the configured limit.
func (bg *behaviorGrader) checkMaxToolCalls(session *models.SessionDigest) []string {
	if bg.maxToolCalls <= 0 {
		return nil
	}

	if session.ToolCallCount > bg.maxToolCalls {
		return []string{fmt.Sprintf("Tool call count %d exceeds max allowed %d", session.ToolCallCount, bg.maxToolCalls)}
	}
	return nil
}

// checkMaxTokens validates total token usage is within the configured limit.
func (bg *behaviorGrader) checkMaxTokens(session *models.SessionDigest) []string {
	if bg.maxTokens <= 0 {
		return nil
	}

	if session.Usage == nil {
		return []string{"No usage data available to check max tokens"}
	} else if total := session.Usage.InputTokens + session.Usage.OutputTokens; total > bg.maxTokens {
		return []string{fmt.Sprintf("Token usage %d exceeds max allowed %d", total, bg.maxTokens)}
	}
	return nil
}

// checkRequiredTools validates that all required tools were used during the session.
func (bg *behaviorGrader) checkRequiredTools(session *models.SessionDigest) []string {
	if len(bg.requiredTools) == 0 {
		return nil
	}

	toolSet := make(map[string]bool, len(session.ToolsUsed))
	for _, t := range session.ToolsUsed {
		toolSet[t] = true
	}

	var failures []string
	for _, req := range bg.requiredTools {
		if !toolSet[req] {
			failures = append(failures, fmt.Sprintf("Required tool not used: %s", req))
		}
	}
	return failures
}

// checkForbiddenTools validates that no forbidden tools were used during the session.
func (bg *behaviorGrader) checkForbiddenTools(session *models.SessionDigest) []string {
	if len(bg.forbiddenTools) == 0 {
		return nil
	}

	toolSet := make(map[string]bool, len(session.ToolsUsed))
	for _, t := range session.ToolsUsed {
		toolSet[t] = true
	}

	var failures []string
	for _, fb := range bg.forbiddenTools {
		if toolSet[fb] {
			failures = append(failures, fmt.Sprintf("Forbidden tool was used: %s", fb))
		}
	}
	return failures
}

// checkMaxDuration validates the task duration is within the configured limit.
func (bg *behaviorGrader) checkMaxDuration(durationMS int64) []string {
	if bg.maxDurationMS <= 0 {
		return nil
	}

	if durationMS > bg.maxDurationMS {
		return []string{fmt.Sprintf("Duration %dms exceeds max allowed %dms", durationMS, bg.maxDurationMS)}
	}
	return nil
}

// countTotalChecks returns the total number of individual checks to be performed.
func (bg *behaviorGrader) countTotalChecks() int {
	total := 0
	if bg.maxToolCalls > 0 {
		total++
	}
	if bg.maxTokens > 0 {
		total++
	}
	total += len(bg.requiredTools)
	total += len(bg.forbiddenTools)
	if bg.maxDurationMS > 0 {
		total++
	}
	return total
}
