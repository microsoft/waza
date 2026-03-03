package graders

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// ToolSpec describes a tool matching rule with an optional arguments pattern.
type ToolSpec struct {
	Tool           string `mapstructure:"tool"`            // regex matched against tool name
	CommandPattern string `mapstructure:"command_pattern"` // optional regex matched against the command argument
	SkillPattern   string `mapstructure:"skill_pattern"`   // optional regex matched against the skill argument
	PathPattern    string `mapstructure:"path_pattern"`    // optional regex matched against the path argument
}

// toolConstraintGrader validates which tools an agent should/shouldn't use
type toolConstraintGrader struct {
	name        string
	expectTools []ToolSpec
	rejectTools []ToolSpec
}

// ToolConstraintGraderConfig holds the mapstructure-decoded parameters.
// expect_tools and reject_tools must be objects with "tool" and optional
// "command_pattern" fields for regex matching.
type ToolConstraintGraderConfig struct {
	ExpectTools []ToolSpec `mapstructure:"expect_tools"`
	RejectTools []ToolSpec `mapstructure:"reject_tools"`
}

func (p ToolConstraintGraderConfig) Config() map[string]any {
	m := map[string]any{
		"expect_tools": p.ExpectTools,
		"reject_tools": p.RejectTools,
	}

	return m
}

// validateToolSpecs ensures each spec has a valid tool regex and optional args regex.
func validateToolSpecs(specs []ToolSpec, fieldName string) ([]ToolSpec, error) {
	normalized := make([]ToolSpec, len(specs))
	copy(normalized, specs)

	for i, spec := range normalized {
		spec.Tool = strings.TrimSpace(spec.Tool)
		if spec.Tool == "" {
			return nil, fmt.Errorf("config.%s[%d].tool: required non-empty string", fieldName, i)
		}

		if _, err := regexp.Compile("(?i)" + spec.Tool); err != nil {
			return nil, fmt.Errorf("config.%s[%d].tool: invalid regex: %w", fieldName, i, err)
		}

		if spec.CommandPattern != "" {
			if _, err := regexp.Compile("(?i)" + spec.CommandPattern); err != nil {
				return nil, fmt.Errorf("config.%s[%d].command_pattern: invalid regex: %w", fieldName, i, err)
			}
		}

		if spec.SkillPattern != "" {
			if _, err := regexp.Compile("(?i)" + spec.SkillPattern); err != nil {
				return nil, fmt.Errorf("config.%s[%d].skill_pattern: invalid regex: %w", fieldName, i, err)
			}
		}

		if spec.PathPattern != "" {
			if _, err := regexp.Compile("(?i)" + spec.PathPattern); err != nil {
				return nil, fmt.Errorf("config.%s[%d].path_pattern: invalid regex: %w", fieldName, i, err)
			}
		}

		normalized[i] = spec
	}

	return normalized, nil
}

// NewToolConstraintGrader creates a toolConstraintGrader from decoded parameters.
func NewToolConstraintGrader(name string, params ToolConstraintGraderConfig) (*toolConstraintGrader, error) {
	if len(params.ExpectTools) == 0 && len(params.RejectTools) == 0 {
		return nil, fmt.Errorf("tool_constraint grader '%s' must have at least one constraint configured", name)
	}

	expectSpecs, err := validateToolSpecs(params.ExpectTools, "expect_tools")
	if err != nil {
		return nil, fmt.Errorf("tool_constraint grader '%s': %w", name, err)
	}
	rejectSpecs, err := validateToolSpecs(params.RejectTools, "reject_tools")
	if err != nil {
		return nil, fmt.Errorf("tool_constraint grader '%s': %w", name, err)
	}

	return &toolConstraintGrader{
		name:        name,
		expectTools: expectSpecs,
		rejectTools: rejectSpecs,
	}, nil
}

func (tc *toolConstraintGrader) Name() string            { return tc.name }
func (tc *toolConstraintGrader) Kind() models.GraderKind { return models.GraderKindToolConstraint }

func (tc *toolConstraintGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		session := gradingContext.Session
		if session == nil {
			return &models.GraderResults{
				Name:     tc.name,
				Type:     models.GraderKindToolConstraint,
				Score:    0.0,
				Passed:   false,
				Feedback: "No session digest available for tool constraint grading",
			}, nil
		}

		var failures []string

		failures = append(failures, tc.checkExpectTools(session)...)
		failures = append(failures, tc.checkRejectTools(session)...)

		totalChecks := tc.countTotalChecks()
		passedChecks := totalChecks - len(failures)

		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}

		feedback := "All tool constraint checks passed"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}

		details := map[string]any{
			"expect_tools": describeToolSpecs(tc.expectTools),
			"reject_tools": describeToolSpecs(tc.rejectTools),
			"failures":     failures,
			"tools_used":   session.ToolsUsed,
		}
		if session.Usage != nil {
			details["tokens_total"] = session.Usage.InputTokens + session.Usage.OutputTokens
			details["total_turns"] = session.Usage.Turns
		}
		return &models.GraderResults{
			Name:     tc.name,
			Type:     models.GraderKindToolConstraint,
			Score:    score,
			Passed:   len(failures) == 0,
			Feedback: feedback,
			Details:  details,
		}, nil
	})
}

// matchesToolCall returns true if spec matches the given tool call constraints.
// NOTE: this function assumes that the regexes have already been validated.
func matchesToolCall(spec ToolSpec, call models.ToolCall) bool {
	checkPattern := func(pattern, text string) bool {
		// empty pattern automatically passes - we validate that they have passed at least one check in
		// validateToolSpecs().
		if pattern == "" {
			return true
		}

		// pre-req that the regex is valid.
		matched, _ := regexp.MatchString("(?i)"+pattern, text)
		return matched
	}

	if !checkPattern(spec.Tool, call.Name) {
		return false
	}

	if !checkPattern(spec.CommandPattern, call.Arguments.Command) {
		return false
	}

	if !checkPattern(spec.PathPattern, call.Arguments.Path) {
		return false
	}

	if !checkPattern(spec.SkillPattern, call.Arguments.Skill) {
		return false
	}

	return true
}

// describeToolSpec returns a human-readable label for a ToolSpec.
func describeToolSpec(spec ToolSpec) string {
	var qualifiers []string

	if spec.CommandPattern != "" {
		qualifiers = append(qualifiers, fmt.Sprintf("command_pattern: %s", spec.CommandPattern))
	}
	if spec.SkillPattern != "" {
		qualifiers = append(qualifiers, fmt.Sprintf("skill_pattern: %s", spec.SkillPattern))
	}
	if spec.PathPattern != "" {
		qualifiers = append(qualifiers, fmt.Sprintf("path_pattern: %s", spec.PathPattern))
	}

	if len(qualifiers) == 0 {
		return spec.Tool
	}

	return fmt.Sprintf("%s (%s)", spec.Tool, strings.Join(qualifiers, ", "))
}

// describeToolSpecs returns human-readable labels for a slice of ToolSpecs.
func describeToolSpecs(specs []ToolSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = describeToolSpec(s)
	}
	return out
}

func (tc *toolConstraintGrader) checkExpectTools(session *models.SessionDigest) []string {
	if len(tc.expectTools) == 0 {
		return nil
	}

	var failures []string
	for _, spec := range tc.expectTools {
		found := false

		for _, call := range session.ToolCalls {
			if matchesToolCall(spec, call) {
				found = true
				break
			}
		}

		if !found {
			failures = append(failures, fmt.Sprintf("Expected tool not used: %s", describeToolSpec(spec)))
		}
	}
	return failures
}

func (tc *toolConstraintGrader) checkRejectTools(session *models.SessionDigest) []string {
	if len(tc.rejectTools) == 0 {
		return nil
	}

	var failures []string
	for _, spec := range tc.rejectTools {
		found := false

		for _, call := range session.ToolCalls {
			if matchesToolCall(spec, call) {
				found = true
				break
			}
		}

		if found {
			failures = append(failures, fmt.Sprintf("Rejected tool was used: %s", describeToolSpec(spec)))
		}
	}
	return failures
}

func (tc *toolConstraintGrader) countTotalChecks() int {
	total := len(tc.expectTools) + len(tc.rejectTools)
	return total
}
