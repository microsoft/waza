package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/spboyer/waza/internal/models"
)

// KeywordGraderArgs holds the arguments for creating a keyword grader.
type KeywordGraderArgs struct {
	// Name is the identifier for this grader, used in results and error messages.
	Name string
	// MustContain lists keywords that must appear in the output (case-insensitive).
	MustContain []string `mapstructure:"must_contain"`
	// Keywords is a backward-compatible alias for MustContain.
	Keywords []string `mapstructure:"keywords"`
	// MustNotContain lists keywords that must NOT appear in the output (case-insensitive).
	MustNotContain []string `mapstructure:"must_not_contain"`
}

// keywordGrader validates output by checking for keyword presence or absence.
type keywordGrader struct {
	name           string
	mustContain    []string
	mustNotContain []string
}

// NewKeywordGrader creates a [keywordGrader] that checks for keyword presence/absence
// in the agent output using case-insensitive matching.
func NewKeywordGrader(args KeywordGraderArgs) (*keywordGrader, error) {
	return &keywordGrader{
		name:           args.Name,
		mustContain:    append(append([]string{}, args.MustContain...), args.Keywords...),
		mustNotContain: args.MustNotContain,
	}, nil
}

func (kg *keywordGrader) Name() string            { return kg.name }
func (kg *keywordGrader) Kind() models.GraderKind { return models.GraderKindKeyword }

func (kg *keywordGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		var failures []string
		outputLower := strings.ToLower(gradingContext.Output)

		for _, keyword := range kg.mustContain {
			if !strings.Contains(outputLower, strings.ToLower(keyword)) {
				failures = append(failures, fmt.Sprintf("Missing expected keyword: %s", keyword))
			}
		}

		for _, keyword := range kg.mustNotContain {
			if strings.Contains(outputLower, strings.ToLower(keyword)) {
				failures = append(failures, fmt.Sprintf("Found forbidden keyword: %s", keyword))
			}
		}

		totalChecks := len(kg.mustContain) + len(kg.mustNotContain)
		passedChecks := totalChecks - len(failures)

		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}

		feedback := "All keyword checks passed"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}

		return &models.GraderResults{
			Name:     kg.name,
			Type:     models.GraderKindKeyword,
			Score:    score,
			Passed:   len(failures) == 0,
			Feedback: feedback,
			Details: map[string]any{
				"must_contain":     kg.mustContain,
				"must_not_contain": kg.mustNotContain,
				"failures":         failures,
			},
		}, nil
	})
}
