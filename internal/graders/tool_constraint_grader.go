package graders

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// toolConstraintGrader validates which tools an agent should/shouldn't use
type toolConstraintGrader struct {
	name        string
	expectTools []models.ToolSpecParameters
	rejectTools []models.ToolSpecParameters

	// allowOnly implements the policy allow-list. When non-nil, every
	// observed tool call must match at least one entry; violations are
	// reported as failures. A non-nil empty slice denies all tool calls.
	allowOnly *[]models.ToolSpecParameters
}

// validateToolSpecs ensures each spec has a valid tool regex and optional args regex.
func validateToolSpecs(specs []models.ToolSpecParameters, fieldName string) ([]models.ToolSpecParameters, error) {
	normalized := make([]models.ToolSpecParameters, len(specs))
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

		// Persist Compile() side-effects back into the map. Map values are not
		// addressable, so iterating gives us a value-copy of each Matcher;
		// calling Compile() on that copy mutates only the local, and Match()
		// would have to recompile on every call. Reassigning the (now-compiled)
		// copy into the map preserves compiledRegex/compiledSchema for hot
		// paths and keeps subsequent Match() calls allocation-free.
		for argName, m := range spec.Args {
			if err := m.Compile(); err != nil {
				return nil, fmt.Errorf("config.%s[%d].args[%s]: %w", fieldName, i, argName, err)
			}
			spec.Args[argName] = m
		}

		normalized[i] = spec
	}

	return normalized, nil
}

// validateAllowOnlySpecs validates an allow-list of tool specs. Unlike
// expect/reject specs, the Tool field is matched exactly (case-insensitive) at
// grade time — it is NOT compiled as a regex — so we only require a non-empty
// name here. The other pattern fields (CommandPattern, SkillPattern,
// PathPattern) are still regexes and still compiled/validated.
func validateAllowOnlySpecs(specs []models.ToolSpecParameters, fieldName string) ([]models.ToolSpecParameters, error) {
	normalized := make([]models.ToolSpecParameters, len(specs))
	copy(normalized, specs)

	for i, spec := range normalized {
		spec.Tool = strings.TrimSpace(spec.Tool)
		if spec.Tool == "" {
			return nil, fmt.Errorf("config.%s[%d].tool: required non-empty string", fieldName, i)
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

		for argName, m := range spec.Args {
			if err := m.Compile(); err != nil {
				return nil, fmt.Errorf("config.%s[%d].args[%s]: %w", fieldName, i, argName, err)
			}
			spec.Args[argName] = m
		}

		normalized[i] = spec
	}

	return normalized, nil
}

// NewToolConstraintGrader creates a toolConstraintGrader from decoded parameters.
func NewToolConstraintGrader(name string, params models.ToolConstraintGraderParameters) (*toolConstraintGrader, error) {
	if len(params.ExpectTools) == 0 && len(params.RejectTools) == 0 && params.AllowOnly == nil {
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

	var allowOnly *[]models.ToolSpecParameters
	if params.AllowOnly != nil {
		allowSpecs, err := validateAllowOnlySpecs(*params.AllowOnly, "allow_only")
		if err != nil {
			return nil, fmt.Errorf("tool_constraint grader '%s': %w", name, err)
		}
		allowOnly = &allowSpecs
	}

	return &toolConstraintGrader{
		name:        name,
		expectTools: expectSpecs,
		rejectTools: rejectSpecs,
		allowOnly:   allowOnly,
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

		constraintFailures := len(failures)
		allowFailures, allowChecks, allowFailedChecks := tc.checkAllowOnly(session)
		failures = append(failures, allowFailures...)

		totalChecks := tc.countTotalChecks() + allowChecks
		passedChecks := totalChecks - constraintFailures - allowFailedChecks

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
		if tc.allowOnly != nil {
			details["allow_only"] = describeToolSpecs(*tc.allowOnly)
			details["allow_only_declared"] = true
			details["allow_only_violations"] = allowOnlyViolationNames(*tc.allowOnly, session.ToolCalls)
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
func matchesToolCall(spec models.ToolSpecParameters, call models.ToolCall) bool {
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

	if len(spec.Args) > 0 {
		args, err := normalizeToolCallArgs(call)
		if err != nil {
			return false
		}
		if failures := evaluateArgMatchers(spec.Args, args); len(failures) > 0 {
			return false
		}
	}

	return true
}

// matchesAllowSpec is like matchesToolCall but matches the Tool field exactly
// (case-insensitive) rather than as a regex. This is the semantics used by
// AllowOnly so that an .agent.md `tools: [bash]` entry cannot inadvertently
// match `bash-experimental` or `bashful`. The other pattern fields
// (CommandPattern etc.) still use regex, so callers can restrict which
// invocations of an allowed tool are permitted.
func matchesAllowSpec(spec models.ToolSpecParameters, call models.ToolCall) bool {
	if !strings.EqualFold(spec.Tool, call.Name) {
		return false
	}

	checkPattern := func(pattern, text string) bool {
		if pattern == "" {
			return true
		}
		matched, _ := regexp.MatchString("(?i)"+pattern, text)
		return matched
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

	if len(spec.Args) > 0 {
		args, err := normalizeToolCallArgs(call)
		if err != nil {
			return false
		}
		if failures := evaluateArgMatchers(spec.Args, args); len(failures) > 0 {
			return false
		}
	}

	return true
}

// describeToolSpec returns a human-readable label for a ToolSpec.
func describeToolSpec(spec models.ToolSpecParameters) string {
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
	if len(spec.Args) > 0 {
		keys := make([]string, 0, len(spec.Args))
		for k := range spec.Args {
			keys = append(keys, k)
		}
		sortStrings(keys)
		qualifiers = append(qualifiers, fmt.Sprintf("args: [%s]", strings.Join(keys, ", ")))
	}

	if len(qualifiers) == 0 {
		return spec.Tool
	}

	return fmt.Sprintf("%s (%s)", spec.Tool, strings.Join(qualifiers, ", "))
}

// describeToolSpecs returns human-readable labels for a slice of ToolSpecs.
func describeToolSpecs(specs []models.ToolSpecParameters) []string {
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

// checkAllowOnly enforces the allow-list policy over every observed tool
// call. Each observed call that fails to match any allow-list entry counts
// as one failed check; each observed call that matches counts as one passed
// check. When there are no observed calls, checkAllowOnly still records one
// passing "policy" check so an agent with an empty session isn't rewarded
// with a divide-by-zero score of 1.0 while other constraints could still be
// failing. When AllowOnly is nil, no checks are added.
func (tc *toolConstraintGrader) checkAllowOnly(
	session *models.SessionDigest,
) (failures []string, totalChecks, failedChecks int) {
	if tc.allowOnly == nil {
		return nil, 0, 0
	}

	calls := session.ToolCalls
	if len(calls) == 0 {
		// Nothing to check — one vacuous pass so the policy is represented
		// in the check count even for zero-tool sessions.
		return nil, 1, 0
	}

	allowed := describeToolSpecs(*tc.allowOnly)

	violationCounts := map[string]int{}
	violationOrder := []string{}
	for _, call := range calls {
		if allowOnlyMatches(*tc.allowOnly, call) {
			continue
		}
		name := call.Name
		if name == "" {
			name = "<unnamed>"
		}
		if _, seen := violationCounts[name]; !seen {
			violationOrder = append(violationOrder, name)
		}
		violationCounts[name]++
		failedChecks++
	}

	for _, name := range violationOrder {
		count := violationCounts[name]
		msg := fmt.Sprintf("Undeclared tool used: %s (not in allow_only: [%s])",
			name, strings.Join(allowed, ", "))
		if count > 1 {
			msg = fmt.Sprintf("Undeclared tool used: %s (%d calls; not in allow_only: [%s])",
				name, count, strings.Join(allowed, ", "))
		}
		failures = append(failures, msg)
	}

	return failures, len(calls), failedChecks
}

// allowOnlyMatches reports whether at least one allow-list entry matches
// the observed call.
func allowOnlyMatches(specs []models.ToolSpecParameters, call models.ToolCall) bool {
	for _, spec := range specs {
		if matchesAllowSpec(spec, call) {
			return true
		}
	}
	return false
}

// allowOnlyViolationNames returns the deduplicated list of tool names in the
// session that failed the allow-only policy. Preserves observation order for
// deterministic reporting.
func allowOnlyViolationNames(specs []models.ToolSpecParameters, calls []models.ToolCall) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, call := range calls {
		if allowOnlyMatches(specs, call) {
			continue
		}
		name := call.Name
		if name == "" {
			name = "<unnamed>"
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func (tc *toolConstraintGrader) countTotalChecks() int {
	total := len(tc.expectTools) + len(tc.rejectTools)
	return total
}
