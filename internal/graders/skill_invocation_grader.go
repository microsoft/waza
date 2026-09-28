package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// skillInvocationGrader compares the agent's actual skill invocation sequence against
// an expected set of skills. It supports three matching modes and calculates
// precision, recall, and F1 scores.
type skillInvocationGrader struct {
	name            string
	matchingMode    models.SkillInvocationMatchingMode
	requiredSkills  []string
	forbiddenSkills []string
	allowExtra      bool
}

// NewSkillInvocationGrader creates a skillInvocationGrader from decoded parameters.
func NewSkillInvocationGrader(name string, params models.SkillInvocationGraderParameters) (*skillInvocationGrader, error) {
	if len(params.RequiredSkills) == 0 && len(params.ForbiddenSkills) == 0 {
		return nil, fmt.Errorf("skill_invocation grader '%s' must have at least one required_skills or forbidden_skills entry", name)
	}

	mode := params.Mode
	if len(params.RequiredSkills) == 0 && mode == "" {
		mode = models.SkillMatchingModeAnyOrder
	}
	switch mode {
	case models.SkillMatchingModeExact, models.SkillMatchingModeInOrder, models.SkillMatchingModeAnyOrder:
		// valid
	default:
		return nil, fmt.Errorf("skill_invocation grader '%s' has invalid mode %q (must be %s, %s, or %s)", name, params.Mode,
			models.SkillMatchingModeExact,
			models.SkillMatchingModeInOrder,
			models.SkillMatchingModeAnyOrder)
	}

	// Default allow_extra to true if not specified
	allowExtra := true
	if params.AllowExtra != nil {
		allowExtra = *params.AllowExtra
	}

	requiredSkills := params.RequiredSkills
	if requiredSkills == nil {
		requiredSkills = []string{}
	}
	forbiddenSkills := params.ForbiddenSkills
	if forbiddenSkills == nil {
		forbiddenSkills = []string{}
	}

	return &skillInvocationGrader{
		name:            name,
		matchingMode:    mode,
		requiredSkills:  requiredSkills,
		forbiddenSkills: forbiddenSkills,
		allowExtra:      allowExtra,
	}, nil
}

func (g *skillInvocationGrader) Name() string            { return g.name }
func (g *skillInvocationGrader) Kind() models.GraderKind { return models.GraderKindSkillInvocation }

func (g *skillInvocationGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		skillInvocations := gradingContext.SkillInvocations
		if skillInvocations == nil {
			skillInvocations = []execution.SkillInvocation{}
		}

		// Extract skill names from invocations
		actual := make([]string, len(skillInvocations))
		for i, si := range skillInvocations {
			actual[i] = si.Name
		}

		// Classify surfacing status for each required skill using the routing
		// catalog snapshot the runtime advertised. This distinguishes
		// "runtime never surfaced the skill" (not skill-side fixable) from
		// "runtime surfaced the skill but the model chose not to invoke it"
		// (skill-side fixable). See issue #540.
		availableNames := make(map[string]bool, len(gradingContext.AvailableSkills))
		for _, as := range gradingContext.AvailableSkills {
			if as.Name != "" {
				availableNames[as.Name] = true
			}
		}
		// availableSkillsKnown is false when the runtime did not report an
		// available-skills snapshot (e.g. legacy or non-Copilot executors);
		// in that case we skip the surfacing classification instead of
		// misreporting every skill as "never surfaced".
		availableSkillsKnown := len(gradingContext.AvailableSkills) > 0
		notSurfaced := []string{}
		surfacedButNotInvoked := []string{}
		if availableSkillsKnown && len(g.requiredSkills) > 0 {
			invoked := make(map[string]bool, len(actual))
			for _, a := range actual {
				invoked[a] = true
			}
			for _, req := range g.requiredSkills {
				if invoked[req] {
					continue
				}
				if availableNames[req] {
					surfacedButNotInvoked = append(surfacedButNotInvoked, req)
				} else {
					notSurfaced = append(notSurfaced, req)
				}
			}
		}

		precision, recall, f1 := 1.0, 1.0, 1.0
		requiredPassed := true
		if len(g.requiredSkills) > 0 {
			precision, recall = g.computePrecisionRecall(actual)
			f1 = computeF1(precision, recall)
			requiredPassed = g.checkMatch(actual)
		}
		forbiddenViolations := g.findForbiddenViolations(actual)
		if forbiddenViolations == nil {
			forbiddenViolations = []string{}
		}
		forbiddenPassed := len(forbiddenViolations) == 0

		// Adjust score based on allow_extra flag
		score := f1
		if !g.allowExtra && len(g.requiredSkills) > 0 && len(actual) > len(g.requiredSkills) {
			// Penalize extra invocations when not allowed
			extraCount := len(actual) - len(g.requiredSkills)
			penalty := float64(extraCount) / float64(len(actual))
			score = f1 * (1.0 - penalty*0.6) // Reduce score by up to 60% for extras
		}
		if !forbiddenPassed {
			score = 0.0
		}

		passed := requiredPassed && forbiddenPassed

		feedback := "Skill invocation sequence matched"
		if !passed {
			feedback = g.buildFailureFeedback(actual, requiredPassed, forbiddenViolations)
			// Append a routing-surface note so trigger-precision suites can
			// tell "description lost the routing contest" apart from
			// "runtime never surfaced the skill" without inferring from
			// token counts (see issue #540).
			if availableSkillsKnown && len(g.requiredSkills) > 0 {
				var suffix string
				switch {
				case len(notSurfaced) > 0 && len(surfacedButNotInvoked) > 0:
					suffix = fmt.Sprintf(
						"; runtime never surfaced: %s; surfaced but not invoked: %s",
						strings.Join(notSurfaced, ", "),
						strings.Join(surfacedButNotInvoked, ", "),
					)
				case len(notSurfaced) > 0:
					suffix = fmt.Sprintf("; runtime never surfaced: %s", strings.Join(notSurfaced, ", "))
				case len(surfacedButNotInvoked) > 0:
					suffix = fmt.Sprintf("; surfaced but not invoked: %s", strings.Join(surfacedButNotInvoked, ", "))
				}
				feedback += suffix
			}
		} else if len(g.requiredSkills) == 0 {
			feedback = "Forbidden skills were not invoked"
		} else if !g.allowExtra && len(actual) > len(g.requiredSkills) {
			// Passed the match but has extra invocations when not allowed
			feedback = fmt.Sprintf("Skill invocation sequence matched but had extra invocations (got %d, expected %d)", len(actual), len(g.requiredSkills))
		}

		details := map[string]any{
			"mode":                 string(g.matchingMode),
			"required_skills":      g.requiredSkills,
			"forbidden_skills":     g.forbiddenSkills,
			"forbidden_violations": forbiddenViolations,
			"actual_skills":        actual,
			"allow_extra":          g.allowExtra,
			"precision":            precision,
			"recall":               recall,
			"f1":                   f1,
		}
		if availableSkillsKnown {
			availableNamesList := make([]string, 0, len(gradingContext.AvailableSkills))
			for _, as := range gradingContext.AvailableSkills {
				if as.Name != "" {
					availableNamesList = append(availableNamesList, as.Name)
				}
			}
			details["available_skills"] = availableNamesList
			details["not_surfaced_required_skills"] = notSurfaced
			details["surfaced_but_not_invoked_required_skills"] = surfacedButNotInvoked
		}

		return &models.GraderResults{
			Name:     g.name,
			Type:     models.GraderKindSkillInvocation,
			Score:    score,
			Passed:   passed,
			Feedback: feedback,
			Details:  details,
		}, nil
	})
}

// checkMatch returns true if the actual sequence satisfies the matching mode constraint.
func (g *skillInvocationGrader) checkMatch(actual []string) bool {
	if len(g.requiredSkills) == 0 {
		return true
	}

	switch g.matchingMode {
	case models.SkillMatchingModeExact:
		return g.exactMatch(actual)
	case models.SkillMatchingModeInOrder:
		return g.inOrderMatch(actual)
	case models.SkillMatchingModeAnyOrder:
		return g.anyOrderMatch(actual)
	default:
		return false
	}
}

// exactMatch checks that actual and required are identical in length, order, and content.
func (g *skillInvocationGrader) exactMatch(actual []string) bool {
	if len(actual) != len(g.requiredSkills) {
		return false
	}
	for i, exp := range g.requiredSkills {
		if actual[i] != exp {
			return false
		}
	}
	return true
}

// inOrderMatch checks that all required skills appear in actual in the correct order,
// allowing extra steps between them.
func (g *skillInvocationGrader) inOrderMatch(actual []string) bool {
	expIdx := 0
	for _, a := range actual {
		if expIdx < len(g.requiredSkills) && a == g.requiredSkills[expIdx] {
			expIdx++
		}
	}
	return expIdx == len(g.requiredSkills)
}

// anyOrderMatch checks that all required skills appear in actual with sufficient frequency,
// regardless of order.
func (g *skillInvocationGrader) anyOrderMatch(actual []string) bool {
	// Build frequency map of required skills
	requiredCounts := make(map[string]int, len(g.requiredSkills))
	for _, e := range g.requiredSkills {
		requiredCounts[e]++
	}

	// Build frequency map of actual skills
	actualCounts := make(map[string]int, len(actual))
	for _, a := range actual {
		actualCounts[a]++
	}

	// Each required skill must appear at least as many times as specified
	for skill, needed := range requiredCounts {
		if actualCounts[skill] < needed {
			return false
		}
	}
	return true
}

// computePrecisionRecall calculates precision and recall based on how many required
// skills were found in the actual sequence.
func (g *skillInvocationGrader) computePrecisionRecall(actual []string) (precision, recall float64) {
	if len(g.requiredSkills) == 0 && len(actual) == 0 {
		return 1.0, 1.0
	}

	// Count how many required skills are present in actual (with frequency awareness)
	requiredCounts := make(map[string]int, len(g.requiredSkills))
	for _, e := range g.requiredSkills {
		requiredCounts[e]++
	}

	actualCounts := make(map[string]int, len(actual))
	for _, a := range actual {
		actualCounts[a]++
	}

	// True positives: min(required count, actual count) for each required skill
	truePositives := 0
	for skill, needed := range requiredCounts {
		got := actualCounts[skill]
		if got > needed {
			got = needed
		}
		truePositives += got
	}

	if len(actual) > 0 {
		precision = float64(truePositives) / float64(len(actual))
	}
	if len(g.requiredSkills) > 0 {
		recall = float64(truePositives) / float64(len(g.requiredSkills))
	}
	return precision, recall
}

func (g *skillInvocationGrader) findForbiddenViolations(actual []string) []string {
	if len(g.forbiddenSkills) == 0 || len(actual) == 0 {
		return nil
	}

	actualCounts := make(map[string]int, len(actual))
	for _, a := range actual {
		actualCounts[a]++
	}

	violations := make([]string, 0, len(g.forbiddenSkills))
	seen := make(map[string]bool, len(g.forbiddenSkills))
	for _, skill := range g.forbiddenSkills {
		if actualCounts[skill] > 0 && !seen[skill] {
			violations = append(violations, skill)
			seen[skill] = true
		}
	}
	return violations
}

// buildFailureFeedback generates a human-readable explanation of why the match failed.
func (g *skillInvocationGrader) buildFailureFeedback(actual []string, requiredPassed bool, forbiddenViolations []string) string {
	var parts []string

	if len(g.requiredSkills) > 0 && !requiredPassed {
		switch g.matchingMode {
		case models.SkillMatchingModeExact:
			parts = append(parts, fmt.Sprintf("Exact match failed: expected %d skills %v, got %d skills %v",
				len(g.requiredSkills), g.requiredSkills, len(actual), actual))
		case models.SkillMatchingModeInOrder:
			parts = append(parts, fmt.Sprintf("In-order match failed: not all required skills %v appeared in order within actual %v",
				g.requiredSkills, actual))
		case models.SkillMatchingModeAnyOrder:
			// Identify which required skills are missing or insufficient
			requiredCounts := make(map[string]int, len(g.requiredSkills))
			for _, e := range g.requiredSkills {
				requiredCounts[e]++
			}
			actualCounts := make(map[string]int, len(actual))
			for _, a := range actual {
				actualCounts[a]++
			}
			var missing []string
			for skill, needed := range requiredCounts {
				got := actualCounts[skill]
				if got < needed {
					missing = append(missing, fmt.Sprintf("%s (need %d, got %d)", skill, needed, got))
				}
			}
			parts = append(parts, fmt.Sprintf("Any-order match failed: missing or insufficient skills: %s",
				strings.Join(missing, ", ")))
		}
	}

	if len(g.requiredSkills) > 0 && !g.allowExtra && len(actual) > len(g.requiredSkills) {
		parts = append(parts, fmt.Sprintf("Extra invocations not allowed (got %d, expected %d)", len(actual), len(g.requiredSkills)))
	}

	if len(forbiddenViolations) > 0 {
		parts = append(parts, fmt.Sprintf("Forbidden skills invoked: %s", strings.Join(forbiddenViolations, ", ")))
	}

	if len(parts) == 0 {
		return "Skill invocation constraints failed"
	}

	return strings.Join(parts, "; ")
}
