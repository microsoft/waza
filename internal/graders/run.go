package graders

import (
	"context"
	"fmt"
	"maps"

	"github.com/microsoft/waza/internal/models"
)

// RunAll runs spec-level graders and task-level validators, returning the
// combined results. judgeModel overrides the model for prompt graders.
func RunAll(ctx context.Context, specGraders []models.GraderConfig, tc *models.TestCase, gCtx *Context, judgeModel string, updateSnapshots bool) (map[string]models.GraderResults, error) {
	results := make(map[string]models.GraderResults)

	for _, vCfg := range specGraders {
		params := cloneParams(vCfg.Parameters)
		if judgeModel != "" && vCfg.Kind == models.GraderKindPrompt {
			params = WithModel(params, judgeModel)
		}
		if updateSnapshots && vCfg.Kind == models.GraderKindDiff {
			params["update_snapshots"] = true
		}
		grader, err := Create(vCfg.Kind, vCfg.Identifier, params)
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
		kind := vCfg.Kind
		if kind == "" {
			return nil, fmt.Errorf("no kind associated with grader %s", vCfg.Identifier)
		}

		params := mergeValidatorParams(vCfg)
		if judgeModel != "" && kind == models.GraderKindPrompt {
			params = WithModel(params, judgeModel)
		}
		if updateSnapshots && kind == models.GraderKindDiff {
			params["update_snapshots"] = true
		}

		grader, err := Create(kind, vCfg.Identifier, params)
		if err != nil {
			return nil, fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)
		}

		result, err := grader.Grade(ctx, gCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)
		}

		w := vCfg.Weight
		if w <= 0 {
			w = 1.0
		}
		result.Weight = w
		results[result.Name] = *result
	}

	return results, nil
}

func cloneParams(params map[string]any) map[string]any {
	if params == nil {
		return make(map[string]any)
	}
	merged := make(map[string]any, len(params))
	maps.Copy(merged, params)
	return merged
}

// mergeValidatorParams returns a copy of the validator's parameters with
// assertions merged in, so the original map is never mutated.
func mergeValidatorParams(v models.ValidatorInline) map[string]any {
	params := cloneParams(v.Parameters)
	if len(v.Checks) > 0 {
		params["assertions"] = v.Checks
	}
	return params
}

// WithModel returns a copy of params with the "model" key set.
func WithModel(params map[string]any, model string) map[string]any {
	merged := make(map[string]any, len(params)+1)
	maps.Copy(merged, params)
	merged["model"] = model
	return merged
}
