package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/spboyer/waza/internal/models"
)

// ActionSequenceMatchingMode controls how actual tool calls are compared to expected actions.
type ActionSequenceMatchingMode string

const (
	MatchingModeExact    ActionSequenceMatchingMode = "exact_match"
	MatchingModeInOrder  ActionSequenceMatchingMode = "in_order_match"
	MatchingModeAnyOrder ActionSequenceMatchingMode = "any_order_match"
)

// actionSequenceGrader compares the agent's actual tool call sequence against
// an expected action path. It supports three matching modes and calculates
// precision, recall, and F1 scores.
type actionSequenceGrader struct {
	name            string
	matchingMode    ActionSequenceMatchingMode
	expectedActions []string
}

// ActionSequenceGraderParams holds the mapstructure-decoded parameters for the action sequence grader.
type ActionSequenceGraderParams struct {
	MatchingMode    string   `mapstructure:"matching_mode"`
	ExpectedActions []string `mapstructure:"expected_actions"`
}

// NewActionSequenceGrader creates an actionSequenceGrader from decoded parameters.
func NewActionSequenceGrader(name string, params ActionSequenceGraderParams) (*actionSequenceGrader, error) {
	if len(params.ExpectedActions) == 0 {
		return nil, fmt.Errorf("action_sequence grader '%s' must have at least one expected_actions entry", name)
	}

	mode := ActionSequenceMatchingMode(params.MatchingMode)
	switch mode {
	case MatchingModeExact, MatchingModeInOrder, MatchingModeAnyOrder:
		// valid
	default:
		return nil, fmt.Errorf("action_sequence grader '%s' has invalid matching_mode %q (must be exact_match, in_order_match, or any_order_match)", name, params.MatchingMode)
	}

	return &actionSequenceGrader{
		name:            name,
		matchingMode:    mode,
		expectedActions: params.ExpectedActions,
	}, nil
}

func (g *actionSequenceGrader) Name() string            { return g.name }
func (g *actionSequenceGrader) Kind() models.GraderKind { return models.GraderKindActionSequence }

func (g *actionSequenceGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		session := gradingContext.Session
		if session == nil {
			return &models.GraderResults{
				Name:     g.name,
				Type:     models.GraderKindActionSequence,
				Score:    0.0,
				Passed:   false,
				Feedback: fmt.Sprintf("action_sequence grader '%s': no session digest available", g.name),
			}, nil
		}

		actual := session.ToolsUsed
		precision, recall := g.computePrecisionRecall(actual)
		f1 := computeF1(precision, recall)

		passed := g.checkMatch(actual)

		feedback := "Action sequence matched"
		if !passed {
			feedback = g.buildFailureFeedback(actual)
		}

		return &models.GraderResults{
			Name:     g.name,
			Type:     models.GraderKindActionSequence,
			Score:    f1,
			Passed:   passed,
			Feedback: feedback,
			Details: map[string]any{
				"matching_mode":    string(g.matchingMode),
				"expected_actions": g.expectedActions,
				"actual_actions":   actual,
				"precision":        precision,
				"recall":           recall,
				"f1":               f1,
			},
		}, nil
	})
}

// checkMatch returns true if the actual sequence satisfies the matching mode constraint.
func (g *actionSequenceGrader) checkMatch(actual []string) bool {
	switch g.matchingMode {
	case MatchingModeExact:
		return g.exactMatch(actual)
	case MatchingModeInOrder:
		return g.inOrderMatch(actual)
	case MatchingModeAnyOrder:
		return g.anyOrderMatch(actual)
	default:
		return false
	}
}

// exactMatch checks that actual and expected are identical in length, order, and content.
func (g *actionSequenceGrader) exactMatch(actual []string) bool {
	if len(actual) != len(g.expectedActions) {
		return false
	}
	for i, exp := range g.expectedActions {
		if actual[i] != exp {
			return false
		}
	}
	return true
}

// inOrderMatch checks that all expected actions appear in actual in the correct order,
// allowing extra steps between them.
func (g *actionSequenceGrader) inOrderMatch(actual []string) bool {
	expIdx := 0
	for _, a := range actual {
		if expIdx < len(g.expectedActions) && a == g.expectedActions[expIdx] {
			expIdx++
		}
	}
	return expIdx == len(g.expectedActions)
}

// anyOrderMatch checks that all expected actions appear in actual with sufficient frequency,
// regardless of order.
func (g *actionSequenceGrader) anyOrderMatch(actual []string) bool {
	// Build frequency map of expected actions
	expectedCounts := make(map[string]int, len(g.expectedActions))
	for _, e := range g.expectedActions {
		expectedCounts[e]++
	}

	// Build frequency map of actual actions
	actualCounts := make(map[string]int, len(actual))
	for _, a := range actual {
		actualCounts[a]++
	}

	// Each expected action must appear at least as many times as specified
	for action, needed := range expectedCounts {
		if actualCounts[action] < needed {
			return false
		}
	}
	return true
}

// computePrecisionRecall calculates precision and recall based on how many expected
// actions were found in the actual sequence.
func (g *actionSequenceGrader) computePrecisionRecall(actual []string) (precision, recall float64) {
	if len(g.expectedActions) == 0 && len(actual) == 0 {
		return 1.0, 1.0
	}

	// Count how many expected actions are present in actual (with frequency awareness)
	expectedCounts := make(map[string]int, len(g.expectedActions))
	for _, e := range g.expectedActions {
		expectedCounts[e]++
	}

	actualCounts := make(map[string]int, len(actual))
	for _, a := range actual {
		actualCounts[a]++
	}

	// True positives: min(expected count, actual count) for each expected action
	truePositives := 0
	for action, needed := range expectedCounts {
		got := actualCounts[action]
		if got > needed {
			got = needed
		}
		truePositives += got
	}

	if len(actual) > 0 {
		precision = float64(truePositives) / float64(len(actual))
	}
	if len(g.expectedActions) > 0 {
		recall = float64(truePositives) / float64(len(g.expectedActions))
	}
	return precision, recall
}

// computeF1 calculates the F1 score from precision and recall.
func computeF1(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0.0
	}
	return 2 * precision * recall / (precision + recall)
}

// buildFailureFeedback generates a human-readable explanation of why the match failed.
func (g *actionSequenceGrader) buildFailureFeedback(actual []string) string {
	var parts []string

	switch g.matchingMode {
	case MatchingModeExact:
		parts = append(parts, fmt.Sprintf("Exact match failed: expected %d actions %v, got %d actions %v",
			len(g.expectedActions), g.expectedActions, len(actual), actual))
	case MatchingModeInOrder:
		parts = append(parts, fmt.Sprintf("In-order match failed: not all expected actions %v appeared in order within actual %v",
			g.expectedActions, actual))
	case MatchingModeAnyOrder:
		// Identify which expected actions are missing or insufficient
		expectedCounts := make(map[string]int, len(g.expectedActions))
		for _, e := range g.expectedActions {
			expectedCounts[e]++
		}
		actualCounts := make(map[string]int, len(actual))
		for _, a := range actual {
			actualCounts[a]++
		}
		var missing []string
		for action, needed := range expectedCounts {
			got := actualCounts[action]
			if got < needed {
				missing = append(missing, fmt.Sprintf("%s (need %d, got %d)", action, needed, got))
			}
		}
		parts = append(parts, fmt.Sprintf("Any-order match failed: missing or insufficient actions: %s",
			strings.Join(missing, ", ")))
	}

	return strings.Join(parts, "; ")
}
