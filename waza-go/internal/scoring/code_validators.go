package scoring

import (
	"fmt"
	"regexp"
	"strings"
	
	"github.com/spboyer/waza/waza-go/internal/models"
)

func init() {
	RegisterValidator("code", NewCodeValidator)
	RegisterValidator("regex", NewRegexValidator)
}

// CodeValidator validates using assertion expressions
type CodeValidator struct {
	identifier string
	assertions []string
}

func NewCodeValidator(identifier string, params map[string]any) Validator {
	assertions := []string{}
	if a, ok := params["assertions"].([]any); ok {
		for _, item := range a {
			if s, ok := item.(string); ok {
				assertions = append(assertions, s)
			}
		}
	}
	
	return &CodeValidator{
		identifier: identifier,
		assertions: assertions,
	}
}

func (v *CodeValidator) Identifier() string { return v.identifier }
func (v *CodeValidator) Category() string   { return "code" }

func (v *CodeValidator) Validate(ctx *ValidationContext) *models.ValidationOut {
	return measureTime(func() *models.ValidationOut {
		if len(v.assertions) == 0 {
			return &models.ValidationOut{
				Identifier: v.identifier,
				Kind:       "code",
				Score:      1.0,
				Passed:     true,
				Feedback:   "No assertions configured",
			}
		}
		
		passed := 0
		var failures []string
		
		// Simple assertion evaluation
		for _, assertion := range v.assertions {
			if evaluateAssertion(assertion, ctx) {
				passed++
			} else {
				failures = append(failures, fmt.Sprintf("Failed: %s", assertion))
			}
		}
		
		score := float64(passed) / float64(len(v.assertions))
		allPassed := len(failures) == 0
		
		feedback := "All assertions passed"
		if !allPassed {
			feedback = strings.Join(failures, "; ")
		}
		
		return &models.ValidationOut{
			Identifier: v.identifier,
			Kind:       "code",
			Score:      score,
			Passed:     allPassed,
			Feedback:   feedback,
			Details: map[string]any{
				"total_assertions":  len(v.assertions),
				"passed_assertions": passed,
				"failures":          failures,
			},
		}
	})
}

// evaluateAssertion is a simple assertion evaluator
func evaluateAssertion(assertion string, ctx *ValidationContext) bool {
	// Simple pattern matching for common assertions
	// In a real implementation, you'd use a proper expression evaluator
	
	// len(output) > N
	if matches := regexp.MustCompile(`len\(output\)\s*>\s*(\d+)`).FindStringSubmatch(assertion); len(matches) > 1 {
		threshold := 0
		fmt.Sscanf(matches[1], "%d", &threshold)
		return len(ctx.Output) > threshold
	}
	
	// "text" in output.lower()
	if matches := regexp.MustCompile(`['"](.+?)['"]\s+in\s+output\.lower\(\)`).FindStringSubmatch(assertion); len(matches) > 1 {
		text := matches[1]
		return strings.Contains(strings.ToLower(ctx.Output), strings.ToLower(text))
	}
	
	// 'text' in output
	if matches := regexp.MustCompile(`['"](.+?)['"]\s+in\s+output`).FindStringSubmatch(assertion); len(matches) > 1 {
		text := matches[1]
		return strings.Contains(ctx.Output, text)
	}
	
	// Default: treat as true for unknown patterns
	return true
}

// RegexValidator validates using regex patterns
type RegexValidator struct {
	identifier    string
	mustMatch     []string
	mustNotMatch  []string
}

func NewRegexValidator(identifier string, params map[string]any) Validator {
	mustMatch := extractStringSlice(params, "must_match")
	mustNotMatch := extractStringSlice(params, "must_not_match")
	
	return &RegexValidator{
		identifier:   identifier,
		mustMatch:    mustMatch,
		mustNotMatch: mustNotMatch,
	}
}

func (v *RegexValidator) Identifier() string { return v.identifier }
func (v *RegexValidator) Category() string   { return "regex" }

func (v *RegexValidator) Validate(ctx *ValidationContext) *models.ValidationOut {
	return measureTime(func() *models.ValidationOut {
		var failures []string
		
		for _, pattern := range v.mustMatch {
			matched, _ := regexp.MatchString(pattern, ctx.Output)
			if !matched {
				failures = append(failures, fmt.Sprintf("Missing expected pattern: %s", pattern))
			}
		}
		
		for _, pattern := range v.mustNotMatch {
			matched, _ := regexp.MatchString(pattern, ctx.Output)
			if matched {
				failures = append(failures, fmt.Sprintf("Found forbidden pattern: %s", pattern))
			}
		}
		
		totalChecks := len(v.mustMatch) + len(v.mustNotMatch)
		passedChecks := totalChecks - len(failures)
		
		score := 1.0
		if totalChecks > 0 {
			score = float64(passedChecks) / float64(totalChecks)
		}
		
		feedback := "All patterns matched"
		if len(failures) > 0 {
			feedback = strings.Join(failures, "; ")
		}
		
		return &models.ValidationOut{
			Identifier: v.identifier,
			Kind:       "regex",
			Score:      score,
			Passed:     len(failures) == 0,
			Feedback:   feedback,
			Details: map[string]any{
				"must_match":     v.mustMatch,
				"must_not_match": v.mustNotMatch,
				"failures":       failures,
			},
		}
	})
}

func extractStringSlice(params map[string]any, key string) []string {
	result := []string{}
	if val, ok := params[key].([]any); ok {
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
	}
	return result
}
