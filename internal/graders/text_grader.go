package graders

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// TextGraderArgs holds the arguments for creating a text grader.
type TextGraderArgs struct {
	Name string

	// Contains lists substrings that must appear in the output (case-insensitive).
	Contains []string `mapstructure:"contains"`
	// NotContains lists substrings that must NOT appear in the output (case-insensitive).
	NotContains []string `mapstructure:"not_contains"`
	// ContainsCS lists substrings that must appear in the output (case-sensitive).
	ContainsCS []string `mapstructure:"contains_cs"`
	// NotContainsCS lists substrings that must NOT appear in the output (case-sensitive).
	NotContainsCS []string `mapstructure:"not_contains_cs"`
	// RegexMatch lists regex patterns that must match somewhere in the output.
	RegexMatch []string `mapstructure:"regex_match"`
	// RegexNotMatch lists regex patterns that must NOT match anywhere in the output.
	RegexNotMatch []string `mapstructure:"regex_not_match"`
}

// TextGrader validates output using substring matching and regex patterns.
type TextGrader struct {
	name          string
	contains      []string
	notContains   []string
	containsCS    []string
	notContainsCS []string
	regexMatch    []string
	regexNotMatch []string
}

// NewTextGrader creates a [TextGrader] that checks for substring presence/absence
// and regex pattern matching in the agent output.
func NewTextGrader(args TextGraderArgs) (*TextGrader, error) {
	return &TextGrader{
		name:          args.Name,
		contains:      args.Contains,
		notContains:   args.NotContains,
		containsCS:    args.ContainsCS,
		notContainsCS: args.NotContainsCS,
		regexMatch:    args.RegexMatch,
		regexNotMatch: args.RegexNotMatch,
	}, nil
}

func (tg *TextGrader) Name() string            { return tg.name }
func (tg *TextGrader) Kind() models.GraderKind { return models.GraderKindText }

func (tg *TextGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		var failures []string
		outputLower := strings.ToLower(gradingContext.Output)

		// Case-insensitive contains
		for _, s := range tg.contains {
			if !strings.Contains(outputLower, strings.ToLower(s)) {
				failures = append(failures, fmt.Sprintf("Missing expected substring: %s", s))
			}
		}

		// Case-insensitive not-contains
		for _, s := range tg.notContains {
			if strings.Contains(outputLower, strings.ToLower(s)) {
				failures = append(failures, fmt.Sprintf("Found forbidden substring: %s", s))
			}
		}

		// Case-sensitive contains
		for _, s := range tg.containsCS {
			if !strings.Contains(gradingContext.Output, s) {
				failures = append(failures, fmt.Sprintf("Missing expected substring (case-sensitive): %s", s))
			}
		}

		// Case-sensitive not-contains
		for _, s := range tg.notContainsCS {
			if strings.Contains(gradingContext.Output, s) {
				failures = append(failures, fmt.Sprintf("Found forbidden substring (case-sensitive): %s", s))
			}
		}

		// Regex must match
		for _, pattern := range tg.regexMatch {
			re, err := regexp.Compile(pattern)
			if err != nil {
				failures = append(failures, fmt.Sprintf("Invalid regex_match pattern %q: %v", pattern, err))
				continue
			}
			if !re.MatchString(gradingContext.Output) {
				failures = append(failures, fmt.Sprintf("Missing expected pattern: %s", pattern))
			}
		}

		// Regex must not match
		for _, pattern := range tg.regexNotMatch {
			re, err := regexp.Compile(pattern)
			if err != nil {
				failures = append(failures, fmt.Sprintf("Invalid regex_not_match pattern %q: %v", pattern, err))
				continue
			}
			if re.MatchString(gradingContext.Output) {
				failures = append(failures, fmt.Sprintf("Found forbidden pattern: %s", pattern))
			}
		}

		totalChecks := len(tg.contains) + len(tg.notContains) +
			len(tg.containsCS) + len(tg.notContainsCS) +
			len(tg.regexMatch) + len(tg.regexNotMatch)
		passedChecks := totalChecks - len(failures)

		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}

		feedback := "All text checks passed"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}

		return &models.GraderResults{
			Name:     tg.name,
			Type:     models.GraderKindText,
			Score:    score,
			Passed:   len(failures) == 0,
			Feedback: feedback,
			Details: map[string]any{
				"contains":        tg.contains,
				"not_contains":    tg.notContains,
				"contains_cs":     tg.containsCS,
				"not_contains_cs": tg.notContainsCS,
				"regex_match":     tg.regexMatch,
				"regex_not_match": tg.regexNotMatch,
				"failures":        failures,
			},
		}, nil
	})
}
