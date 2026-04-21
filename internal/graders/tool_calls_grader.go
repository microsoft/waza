package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// ToolCallsGrader validates which tools an agent called during execution.
// It checks required tools, forbidden tools, minimum calls, and maximum calls.
type ToolCallsGrader struct {
	name   string
	params models.ToolCallsGraderParameters
}

// NewToolCallsGrader creates a new ToolCallsGrader, returning an error if the
// parameters are invalid (e.g. no constraints defined, negative bounds, or
// min > max).
func NewToolCallsGrader(name string, params models.ToolCallsGraderParameters) (*ToolCallsGrader, error) {
	hasConstraint := len(params.RequiredTools) > 0 ||
		len(params.ForbiddenTools) > 0 ||
		params.MinCalls != nil ||
		params.MaxCalls != nil

	if !hasConstraint {
		return nil, fmt.Errorf("tool_calls grader %q: at least one constraint (required_tools, forbidden_tools, min_calls, max_calls) must be specified", name)
	}

	if params.MinCalls != nil && *params.MinCalls < 0 {
		return nil, fmt.Errorf("tool_calls grader %q: min_calls must be non-negative, got %d", name, *params.MinCalls)
	}
	if params.MaxCalls != nil && *params.MaxCalls < 0 {
		return nil, fmt.Errorf("tool_calls grader %q: max_calls must be non-negative, got %d", name, *params.MaxCalls)
	}
	if params.MinCalls != nil && params.MaxCalls != nil && *params.MinCalls > *params.MaxCalls {
		return nil, fmt.Errorf("tool_calls grader %q: min_calls (%d) must be <= max_calls (%d)", name, *params.MinCalls, *params.MaxCalls)
	}

	return &ToolCallsGrader{name: name, params: params}, nil
}

func (g *ToolCallsGrader) Name() string            { return g.name }
func (g *ToolCallsGrader) Kind() models.GraderKind { return models.GraderKindToolCalls }

func (g *ToolCallsGrader) Grade(_ context.Context, gCtx *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		if gCtx.Session == nil {
			return &models.GraderResults{
				Name:     g.name,
				Passed:   false,
				Score:    0,
				Feedback: "no session data available for tool_calls grading",
			}, nil
		}

		calledSet := make(map[string]bool, len(gCtx.Session.ToolCalls))
		for _, tc := range gCtx.Session.ToolCalls {
			calledSet[tc.Name] = true
		}
		totalCalls := len(gCtx.Session.ToolCalls)

		var totalChecks, passedChecks int
		var failures []string

		for _, tool := range g.params.RequiredTools {
			totalChecks++
			if calledSet[tool] {
				passedChecks++
			} else {
				failures = append(failures, fmt.Sprintf("required tool %q was not called", tool))
			}
		}

		for _, tool := range g.params.ForbiddenTools {
			totalChecks++
			if calledSet[tool] {
				failures = append(failures, fmt.Sprintf("forbidden tool %q was called", tool))
			} else {
				passedChecks++
			}
		}

		if g.params.MinCalls != nil {
			totalChecks++
			if totalCalls >= *g.params.MinCalls {
				passedChecks++
			} else {
				failures = append(failures, fmt.Sprintf("expected at least %d tool calls, got %d", *g.params.MinCalls, totalCalls))
			}
		}

		if g.params.MaxCalls != nil {
			totalChecks++
			if totalCalls <= *g.params.MaxCalls {
				passedChecks++
			} else {
				failures = append(failures, fmt.Sprintf("expected at most %d tool calls, got %d", *g.params.MaxCalls, totalCalls))
			}
		}

		score := float64(passedChecks) / float64(totalChecks)
		passed := passedChecks == totalChecks

		feedback := "all tool_calls checks passed"
		if !passed {
			feedback = strings.Join(failures, "; ")
		}

		calledNames := make([]string, 0, len(calledSet))
		for name := range calledSet {
			calledNames = append(calledNames, name)
		}

		return &models.GraderResults{
			Name:     g.name,
			Passed:   passed,
			Score:    score,
			Feedback: feedback,
			Details: map[string]any{
				"total_calls":   totalCalls,
				"unique_tools":  calledNames,
				"passed_checks": passedChecks,
				"total_checks":  totalChecks,
			},
		}, nil
	})
}
