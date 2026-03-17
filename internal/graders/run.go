package graders

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/models"
)

// RunAll runs spec-level graders and task-level validators, returning the
// combined results. judgeModel overrides the model for prompt graders.
func RunAll(ctx context.Context, specGraders []models.GraderConfig, tc *models.TaskSpec, gCtx *Context, judgeModel string, updateSnapshots bool) (map[string]models.GraderResults, error) {
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

	for _, vCfg := range tc.Graders {
		if vCfg.Type == "" {
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

	return results, nil
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
