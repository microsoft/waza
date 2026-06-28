package graders

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
)

// ToolCallsGrader validates which tools an agent called during execution.
// It checks required tools, forbidden tools, minimum calls, and maximum calls.
type ToolCallsGrader struct {
	name           string
	params         models.ToolCallsGraderParameters
	compiledExpect []compiledExpectation
}

type compiledExpectation struct {
	raw     models.ToolExpectation
	toolRe  *regexp.Regexp
	matcher map[string]argmatcher.Matcher
}

// NewToolCallsGrader creates a new ToolCallsGrader, returning an error if the
// parameters are invalid (e.g. no constraints defined, negative bounds, or
// min > max).
func NewToolCallsGrader(name string, params models.ToolCallsGraderParameters) (*ToolCallsGrader, error) {
	hasConstraint := len(params.RequiredTools) > 0 ||
		len(params.ForbiddenTools) > 0 ||
		params.MinCalls != nil ||
		params.MaxCalls != nil ||
		len(params.Expect) > 0

	if !hasConstraint {
		return nil, fmt.Errorf("tool_calls grader %q: at least one constraint (required_tools, forbidden_tools, min_calls, max_calls, expect) must be specified", name)
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

	compiled := make([]compiledExpectation, 0, len(params.Expect))
	for i, exp := range params.Expect {
		if strings.TrimSpace(exp.Tool) == "" {
			return nil, fmt.Errorf("tool_calls grader %q: expect[%d].tool: required non-empty string", name, i)
		}
		re, err := compileToolRegex(exp.Tool)
		if err != nil {
			return nil, fmt.Errorf("tool_calls grader %q: expect[%d].tool: invalid regex: %w", name, i, err)
		}
		mm := make(map[string]argmatcher.Matcher, len(exp.Args))
		for k, m := range exp.Args {
			if err := m.Compile(); err != nil {
				return nil, fmt.Errorf("tool_calls grader %q: expect[%d].args[%s]: %w", name, i, k, err)
			}
			mm[k] = m
		}
		compiled = append(compiled, compiledExpectation{raw: exp, toolRe: re, matcher: mm})
	}

	return &ToolCallsGrader{name: name, params: params, compiledExpect: compiled}, nil
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

		// Per-expectation checks: each expectation contributes one check;
		// it passes when at least one recorded tool call matches the tool
		// name regex AND all configured arg matchers pass on that call.
		expectResults := make([]map[string]any, 0, len(g.compiledExpect))
		for _, exp := range g.compiledExpect {
			totalChecks++
			matched, detail := evaluateExpectation(exp, gCtx.Session.ToolCalls)
			expectResults = append(expectResults, detail)
			if matched {
				passedChecks++
			} else {
				failures = append(failures, fmt.Sprintf("expectation tool=%q not satisfied: %s", exp.raw.Tool, detail["reason"]))
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

		details := map[string]any{
			"total_calls":   totalCalls,
			"unique_tools":  calledNames,
			"passed_checks": passedChecks,
			"total_checks":  totalChecks,
		}
		if len(expectResults) > 0 {
			details["expect"] = expectResults
		}

		return &models.GraderResults{
			Name:     g.name,
			Passed:   passed,
			Score:    score,
			Feedback: feedback,
			Details:  details,
		}, nil
	})
}

// evaluateExpectation returns whether any of the calls satisfies the
// expectation, and a structured detail record describing the best-effort
// reason on failure (or the index of the satisfying call on success).
func evaluateExpectation(exp compiledExpectation, calls []models.ToolCall) (bool, map[string]any) {
	detail := map[string]any{"tool": exp.raw.Tool}
	if len(exp.matcher) > 0 {
		detail["args"] = exp.raw.Args
	}

	nameMatches := 0
	var lastReason string
	for i, call := range calls {
		if exp.toolRe != nil && !exp.toolRe.MatchString(call.Name) {
			continue
		}
		nameMatches++
		if len(exp.matcher) == 0 {
			detail["matched_call_index"] = i
			detail["matched_call_id"] = call.ID
			return true, detail
		}
		args, err := normalizeToolCallArgs(call)
		if err != nil {
			lastReason = err.Error()
			continue
		}
		failures := evaluateArgMatchers(exp.matcher, args)
		if len(failures) == 0 {
			detail["matched_call_index"] = i
			detail["matched_call_id"] = call.ID
			return true, detail
		}
		lastReason = strings.Join(failures, "; ")
	}

	if nameMatches == 0 {
		detail["reason"] = "no tool call with matching name"
	} else if lastReason != "" {
		detail["reason"] = lastReason
	} else {
		detail["reason"] = "no matching call"
	}
	return false, detail
}
