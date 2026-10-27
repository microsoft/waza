package orchestration

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/models"
)

// ValidateInlineConfiguration is the bounded public runner's execution profile.
// It shares restrictions with controlled collection without claiming that a
// native observation is controlled mock evidence or assurance certification.
func ValidateInlineConfiguration(spec *models.EvalSpec, task *models.TestCase) error {
	if spec == nil || task == nil {
		return fmt.Errorf("inline execution requires an eval and task")
	}
	if spec.Config.EngineType != "mock" && spec.Config.EngineType != "copilot-sdk" {
		return fmt.Errorf("inline execution requires executor mock or copilot-sdk")
	}
	if !spec.SkillsDisabledForTask(task.SkillPaths) {
		return fmt.Errorf("inline execution requires ambient skills explicitly disabled")
	}
	return validateSingleTurnTextConfiguration(spec, task)
}

// ExecuteInlineAttempt bypasses benchmark discovery, caching and retry loops.
// The caller owns initialization, deadline, shutdown and terminalization.
func (r *EvalRunner) ExecuteInlineAttempt(ctx context.Context, task *models.TestCase, origin models.EvidenceOrigin) (*models.RunResult, error) {
	if r == nil || r.cfg == nil {
		return nil, fmt.Errorf("inline attempt requires a configured runner")
	}
	if err := ValidateInlineConfiguration(r.cfg.Spec(), task); err != nil {
		return nil, err
	}
	if r.cache != nil || r.skipGraders || origin.EvalID == "" || origin.TaskID != task.TestID ||
		origin.RunNumber < 1 || origin.AttemptCount < 1 || origin.PriorAttempts != "" {
		return nil, fmt.Errorf("inline attempt requires exact uncached graded origin")
	}
	request, err := r.buildExecutionRequest(task)
	if err != nil {
		return nil, err
	}
	input, err := freezeControlledInput(request)
	if err != nil {
		return nil, err
	}
	frozen, err := input.View()
	if err != nil {
		return nil, err
	}
	previous := r.evalRunID
	r.evalRunID = origin.EvalID
	defer func() { r.evalRunID = previous }()
	run := r.executeRunWithAttempt(ctx, task, origin.RunNumber, origin.AttemptCount, frozen)
	return &run, nil
}
