package graders

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/spboyer/waza/internal/models"
)

// RegexGrader validates using regex patterns
type RegexGrader struct {
	name         string
	mustMatch    []string
	mustNotMatch []string
}

func NewRegexGrader(name string, mustMatch []string, mustNotMatch []string) (*RegexGrader, error) {
	return &RegexGrader{
		name:         name,
		mustMatch:    mustMatch,
		mustNotMatch: mustNotMatch,
	}, nil
}

func (reg *RegexGrader) Name() string { return reg.name }
func (reg *RegexGrader) Type() Type   { return TypeRegex }

func (reg *RegexGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		var failures []string

		for _, pattern := range reg.mustMatch {
			re, err := regexp.Compile(pattern)
			if err != nil {
				failures = append(failures, fmt.Sprintf("Invalid 'must_match' regex pattern %q: %v", pattern, err))
				continue
			}

			if !re.MatchString(gradingContext.Output) {
				failures = append(failures, fmt.Sprintf("Missing expected pattern: %s", pattern))
			}
		}

		for _, pattern := range reg.mustNotMatch {
			re, err := regexp.Compile(pattern)
			if err != nil {
				failures = append(failures, fmt.Sprintf("Invalid 'must_not_match' regex pattern %q: %v", pattern, err))
				continue
			}

			if re.MatchString(gradingContext.Output) {
				failures = append(failures, fmt.Sprintf("Found forbidden pattern: %s", pattern))
			}
		}

		totalChecks := len(reg.mustMatch) + len(reg.mustNotMatch)
		passedChecks := totalChecks - len(failures)

		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}

		feedback := "All patterns matched"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}

		return &models.GraderResults{
			Name:     reg.name,
			Type:     string(TypeRegex),
			Score:    score,
			Passed:   len(failures) == 0,
			Feedback: feedback,
			Details: map[string]any{
				"must_match":     reg.mustMatch,
				"must_not_match": reg.mustNotMatch,
				"failures":       failures,
			},
		}, nil
	})
}
