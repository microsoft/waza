package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
)

// SkillInvocationMatchingMode controls how actual skill invocations are compared to expected skills.
type SkillInvocationMatchingMode string

const (
	SkillMatchingModeExact    SkillInvocationMatchingMode = "exact_match"
	SkillMatchingModeInOrder  SkillInvocationMatchingMode = "in_order"
	SkillMatchingModeAnyOrder SkillInvocationMatchingMode = "any_order"
)

// skillInvocationGrader compares the agent's actual skill invocation sequence against
// an expected set of skills. It supports three matching modes and calculates
// precision, recall, and F1 scores.
type skillInvocationGrader struct {
	name          string
	matchingMode  SkillInvocationMatchingMode
	requiredSkills []string
	allowExtra    bool
}

// SkillInvocationGraderParams holds the mapstructure-decoded parameters for the skill invocation grader.
type SkillInvocationGraderParams struct {
	RequiredSkills []string `mapstructure:"required_skills"`
	Mode           string   `mapstructure:"mode"`
	AllowExtra     *bool    `mapstructure:"allow_extra"`
}

// NewSkillInvocationGrader creates a skillInvocationGrader from decoded parameters.
func NewSkillInvocationGrader(name string, params SkillInvocationGraderParams) (*skillInvocationGrader, error) {
	if len(params.RequiredSkills) == 0 {
		return nil, fmt.Errorf("skill_invocation grader '%s' must have at least one required_skills entry", name)
	}

	mode := SkillInvocationMatchingMode(params.Mode)
	switch mode {
	case SkillMatchingModeExact, SkillMatchingModeInOrder, SkillMatchingModeAnyOrder:
		// valid
	default:
		return nil, fmt.Errorf("skill_invocation grader '%s' has invalid mode %q (must be exact_match, in_order, or any_order)", name, params.Mode)
	}

	// Default allow_extra to true if not specified
	allowExtra := true
	if params.AllowExtra != nil {
		allowExtra = *params.AllowExtra
	}

	return &skillInvocationGrader{
		name:          name,
		matchingMode:  mode,
		requiredSkills: params.RequiredSkills,
		allowExtra:    allowExtra,
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

		precision, recall := g.computePrecisionRecall(actual)
		f1 := computeF1(precision, recall)

		// Adjust score based on allow_extra flag
		score := f1
		if !g.allowExtra && len(actual) > len(g.requiredSkills) {
			// Penalize extra invocations when not allowed
			extraCount := len(actual) - len(g.requiredSkills)
			penalty := float64(extraCount) / float64(len(actual))
			score = f1 * (1.0 - penalty*0.6) // Reduce score by up to 60% for extras
		}

		passed := g.checkMatch(actual)

		feedback := "Skill invocation sequence matched"
		if !passed {
			feedback = g.buildFailureFeedback(actual)
		} else if !g.allowExtra && len(actual) > len(g.requiredSkills) {
			// Passed the match but has extra invocations when not allowed
			feedback = fmt.Sprintf("Skill invocation sequence matched but had extra invocations (got %d, expected %d)", len(actual), len(g.requiredSkills))
		}

		return &models.GraderResults{
			Name:     g.name,
			Type:     models.GraderKindSkillInvocation,
			Score:    score,
			Passed:   passed,
			Feedback: feedback,
			Details: map[string]any{
				"mode":            string(g.matchingMode),
				"required_skills": g.requiredSkills,
				"actual_skills":   actual,
				"allow_extra":     g.allowExtra,
				"precision":       precision,
				"recall":          recall,
				"f1":              f1,
			},
		}, nil
	})
}

// checkMatch returns true if the actual sequence satisfies the matching mode constraint.
func (g *skillInvocationGrader) checkMatch(actual []string) bool {
	switch g.matchingMode {
	case SkillMatchingModeExact:
		return g.exactMatch(actual)
	case SkillMatchingModeInOrder:
		return g.inOrderMatch(actual)
	case SkillMatchingModeAnyOrder:
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

// buildFailureFeedback generates a human-readable explanation of why the match failed.
func (g *skillInvocationGrader) buildFailureFeedback(actual []string) string {
	var parts []string

	switch g.matchingMode {
	case SkillMatchingModeExact:
		parts = append(parts, fmt.Sprintf("Exact match failed: expected %d skills %v, got %d skills %v",
			len(g.requiredSkills), g.requiredSkills, len(actual), actual))
	case SkillMatchingModeInOrder:
		parts = append(parts, fmt.Sprintf("In-order match failed: not all required skills %v appeared in order within actual %v",
			g.requiredSkills, actual))
	case SkillMatchingModeAnyOrder:
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

	if !g.allowExtra && len(actual) > len(g.requiredSkills) {
		parts = append(parts, fmt.Sprintf("Extra invocations not allowed (got %d, expected %d)", len(actual), len(g.requiredSkills)))
	}

	return strings.Join(parts, "; ")
}
