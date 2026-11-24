package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// RunError identifies the failed grader while preserving its error text and cause.
type RunError struct {
	Kind models.GraderKind
	err  error
}

func (e *RunError) Error() string { return e.err.Error() }
func (e *RunError) Unwrap() error { return e.err }

// RunAll runs spec-level graders and task-level validators, returning the
// combined results. Judge settings provide defaults for prompt graders. When a
// grader cannot be created or run, RunAll stops and returns the results of the
// graders that already completed (nil when none did) with a *RunError. No result
// is synthesized for the failing grader, later graders, or expectations.
func RunAll(ctx context.Context, specGraders []models.GraderConfig, tc *models.TestCase, gCtx *Context, judgeModel, judgeReasoningEffort string, updateSnapshots bool) (map[string]models.GraderResults, error) {
	return runAll(ctx, specGraders, tc, gCtx, judgeModel, judgeReasoningEffort, updateSnapshots, Create)
}

// IsPairwisePromptGrader reports whether a grader config selects pairwise prompt grading.
func IsPairwisePromptGrader(kind models.GraderKind, parameters models.GraderParameters) bool {
	if kind != models.GraderKindPrompt {
		return false
	}
	prompt, ok := parameters.(models.PromptGraderParameters)
	return ok && prompt.Mode == models.PromptGraderModePairwise
}

// HasPairwisePromptGraders reports whether the spec or task configures pairwise prompt grading.
func HasPairwisePromptGraders(specGraders []models.GraderConfig, tc *models.TestCase) bool {
	for _, cfg := range specGraders {
		if IsPairwisePromptGrader(cfg.Kind, cfg.Parameters) {
			return true
		}
	}
	if tc != nil {
		for _, cfg := range tc.Validators {
			if IsPairwisePromptGrader(cfg.Kind, cfg.Parameters) {
				return true
			}
		}
	}
	return false
}

// WithoutPairwisePromptGraders returns spec graders other than pairwise prompt graders.
func WithoutPairwisePromptGraders(configs []models.GraderConfig) []models.GraderConfig {
	filtered := make([]models.GraderConfig, 0, len(configs))
	for _, cfg := range configs {
		if !IsPairwisePromptGrader(cfg.Kind, cfg.Parameters) {
			filtered = append(filtered, cfg)
		}
	}
	return filtered
}

// WithoutPairwisePromptValidators returns task validators other than pairwise prompt graders.
func WithoutPairwisePromptValidators(configs []models.ValidatorInline) []models.ValidatorInline {
	filtered := make([]models.ValidatorInline, 0, len(configs))
	for _, cfg := range configs {
		if !IsPairwisePromptGrader(cfg.Kind, cfg.Parameters) {
			filtered = append(filtered, cfg)
		}
	}
	return filtered
}

// ValidatePairwiseGraderNames rejects result-name collisions involving a
// pairwise grader, since RunAll stores results by their effective name.
func ValidatePairwiseGraderNames(specGraders []models.GraderConfig, tc *models.TestCase) error {
	type namedGrader struct {
		pairwise bool
		scope    string
	}
	names := make(map[string]namedGrader)
	add := func(name string, pairwise bool, scope string) error {
		if previous, ok := names[name]; ok && (pairwise || previous.pairwise) {
			return fmt.Errorf("pairwise grader name collision %q between %s and %s graders", name, previous.scope, scope)
		}
		names[name] = namedGrader{pairwise: pairwise, scope: scope}
		return nil
	}
	for _, cfg := range specGraders {
		if err := add(cfg.Identifier, IsPairwisePromptGrader(cfg.Kind, cfg.Parameters), "eval-level"); err != nil {
			return err
		}
	}
	if tc != nil {
		for _, cfg := range tc.Validators {
			if err := add(cfg.Identifier, IsPairwisePromptGrader(cfg.Kind, cfg.Parameters), "task-level"); err != nil {
				return err
			}
		}
		expectations := []struct {
			name  string
			count int
		}{
			{name: "_output_contains", count: len(tc.Expectation.MustInclude)},
			{name: "_output_not_contains", count: len(tc.Expectation.MustExclude)},
			{name: "_output_contains_any", count: len(tc.Expectation.MayInclude)},
		}
		for _, expectation := range expectations {
			if expectation.count == 0 {
				continue
			}
			if err := add(expectation.name, false, "expectation-level"); err != nil {
				return err
			}
		}
	}
	return nil
}

func runAll(ctx context.Context, specGraders []models.GraderConfig, tc *models.TestCase, gCtx *Context, judgeModel, judgeReasoningEffort string, updateSnapshots bool, create func(string, models.GraderParameters) (Grader, error)) (map[string]models.GraderResults, error) {
	if err := ValidatePairwiseGraderNames(specGraders, tc); err != nil {
		return nil, err
	}

	results := make(map[string]models.GraderResults)

	for _, vCfg := range specGraders {
		params := applyDefaults(vCfg.Parameters, judgeModel, judgeReasoningEffort, updateSnapshots)
		grader, err := create(vCfg.Identifier, params)
		if err != nil {
			return completedResults(results), &RunError{Kind: vCfg.Kind, err: fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)}
		}

		result, err := grader.Grade(ctx, gCtx)
		if err != nil {
			return completedResults(results), &RunError{Kind: grader.Kind(), err: fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)}
		}

		result.Weight = vCfg.EffectiveWeight()
		results[result.Name] = *result
	}

	for _, vCfg := range tc.Validators {
		if vCfg.Kind == "" {
			return completedResults(results), &RunError{Kind: vCfg.Kind, err: fmt.Errorf("no kind associated with grader %s", vCfg.Identifier)}
		}

		params := applyDefaults(vCfg.Parameters, judgeModel, judgeReasoningEffort, updateSnapshots)
		grader, err := create(vCfg.Identifier, params)
		if err != nil {
			return completedResults(results), &RunError{Kind: vCfg.Kind, err: fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)}
		}

		result, err := grader.Grade(ctx, gCtx)
		if err != nil {
			return completedResults(results), &RunError{Kind: grader.Kind(), err: fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)}
		}

		result.Weight = vCfg.EffectiveWeight()
		results[result.Name] = *result
	}

	// Evaluate expectation-level text checks (MustInclude, MustExclude, MayInclude)
	for k, v := range evaluateExpectations(tc, gCtx) {
		results[k] = v
	}

	return results, nil
}

// completedResults returns the results recorded before a grader error, or nil
// when no grader completed.
func completedResults(results map[string]models.GraderResults) map[string]models.GraderResults {
	if len(results) == 0 {
		return nil
	}
	return results
}

// evaluateExpectations synthesizes grader results from the expectation-level
// text-match fields: MustInclude (output_contains), MustExclude
// (output_not_contains), and MayInclude (output_contains_any).
func evaluateExpectations(tc *models.TestCase, gCtx *Context) map[string]models.GraderResults {
	results := make(map[string]models.GraderResults)
	exp := tc.Expectation
	output := strings.ToLower(gCtx.Output)

	// MustInclude — all listed strings must appear
	if len(exp.MustInclude) > 0 {
		matched := 0
		var missing []string
		for _, s := range exp.MustInclude {
			if strings.Contains(output, strings.ToLower(s)) {
				matched++
			} else {
				missing = append(missing, s)
			}
		}
		score := float64(matched) / float64(len(exp.MustInclude))
		feedback := fmt.Sprintf("matched %d/%d required strings", matched, len(exp.MustInclude))
		if len(missing) > 0 {
			feedback += fmt.Sprintf("; missing: %v", missing)
		}
		results["_output_contains"] = models.GraderResults{
			Name:     "_output_contains",
			Score:    score,
			Passed:   score == 1.0,
			Feedback: feedback,
			Weight:   1.0,
		}
	}

	// MustExclude — none of the listed strings may appear
	if len(exp.MustExclude) > 0 {
		found := 0
		var present []string
		for _, s := range exp.MustExclude {
			if strings.Contains(output, strings.ToLower(s)) {
				found++
				present = append(present, s)
			}
		}
		score := float64(len(exp.MustExclude)-found) / float64(len(exp.MustExclude))
		feedback := fmt.Sprintf("%d/%d excluded strings absent", len(exp.MustExclude)-found, len(exp.MustExclude))
		if len(present) > 0 {
			feedback += fmt.Sprintf("; found: %v", present)
		}
		results["_output_not_contains"] = models.GraderResults{
			Name:     "_output_not_contains",
			Score:    score,
			Passed:   score == 1.0,
			Feedback: feedback,
			Weight:   1.0,
		}
	}

	// MayInclude — at least one listed string must appear (binary)
	if len(exp.MayInclude) > 0 {
		foundAny := false
		var matched string
		for _, s := range exp.MayInclude {
			if strings.Contains(output, strings.ToLower(s)) {
				foundAny = true
				matched = s
				break
			}
		}
		score := boolToFloat(foundAny)
		feedback := "none of the accepted strings found"
		if foundAny {
			feedback = fmt.Sprintf("found accepted string: %q", matched)
		}
		results["_output_contains_any"] = models.GraderResults{
			Name:     "_output_contains_any",
			Score:    score,
			Passed:   foundAny,
			Feedback: feedback,
			Weight:   1.0,
		}
	}

	return results
}

func boolToFloat(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

func applyDefaults(gp models.GraderParameters, judgeModel, judgeReasoningEffort string, updateSnapshots bool) models.GraderParameters {
	switch p := gp.(type) {
	case models.PromptGraderParameters:
		if judgeModel != "" && p.Model == "" {
			p.Model = judgeModel
		}
		if judgeReasoningEffort != "" && p.ReasoningEffort == "" {
			p.ReasoningEffort = judgeReasoningEffort
		}
		return p
	case models.DiffGraderParameters:
		if updateSnapshots {
			p.UpdateSnapshots = true
		}
		return p
	default:
		return p
	}
}
