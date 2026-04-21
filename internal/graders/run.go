package graders

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// RunAll runs spec-level graders and task-level validators, returning the
// combined results. judgeModel overrides the model for prompt graders.
func RunAll(ctx context.Context, specGraders []models.GraderConfig, tc *models.TestCase, gCtx *Context, judgeModel string, updateSnapshots bool) (map[string]models.GraderResults, error) {
	results := make(map[string]models.GraderResults)

	for _, vCfg := range specGraders {
		params := applyDefaults(vCfg.Parameters, judgeModel, updateSnapshots)
		grader, err := Create(vCfg.Identifier, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)
		}

		result, err := grader.Grade(ctx, gCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)
		}

		result.Weight = vCfg.EffectiveWeight()
		results[result.Name] = *result
	}

	for _, vCfg := range tc.Validators {
		if vCfg.Kind == "" {
			return nil, fmt.Errorf("no kind associated with grader %s", vCfg.Identifier)
		}

		params := applyDefaults(vCfg.Parameters, judgeModel, updateSnapshots)
		grader, err := Create(vCfg.Identifier, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)
		}

		result, err := grader.Grade(ctx, gCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)
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

func applyDefaults(gp models.GraderParameters, judgeModel string, updateSnapshots bool) models.GraderParameters {
	switch p := gp.(type) {
	case models.PromptGraderParameters:
		if judgeModel != "" && p.Model == "" {
			p.Model = judgeModel
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
