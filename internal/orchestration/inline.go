package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
)

// ValidateInlineConfiguration is the bounded runner mock/text profile. It does
// not broaden the historical controlled collector or local benchmark behavior.
func ValidateInlineConfiguration(spec *models.EvalSpec, task *models.TestCase) error {
	if err := ValidateControlledConfiguration(spec, task); err != nil {
		return err
	}
	for _, value := range []int{spec.Config.TrialsPerTask, spec.Config.MaxAttempts, spec.Config.TimeoutSec, spec.Config.FirstEventTimeoutSec} {
		if value < 0 || int64(value) > 2147483647 {
			return fmt.Errorf("inline execution counts and timeouts must fit nonnegative signed 32-bit integers")
		}
	}
	for _, value := range []*int{task.TimeoutSec, task.FirstEventTimeoutSec} {
		if value != nil && (*value < 0 || int64(*value) > 2147483647) {
			return fmt.Errorf("inline task timeouts must fit nonnegative signed 32-bit integers")
		}
	}
	for _, g := range spec.Graders {
		if err := graders.ValidateConfig(g.Identifier, g.Parameters); err != nil {
			return fmt.Errorf("grader %q: %w", g.Identifier, err)
		}
	}
	for _, g := range task.Validators {
		if err := graders.ValidateConfig(g.Identifier, g.Parameters); err != nil {
			return fmt.Errorf("grader %q: %w", g.Identifier, err)
		}
	}
	for _, list := range [][]string{task.Expectation.MustInclude, task.Expectation.MustExclude, task.Expectation.MayInclude} {
		for _, assertion := range list {
			if strings.TrimSpace(assertion) == "" {
				return fmt.Errorf("inline execution requires nonempty expected-output assertions")
			}
		}
	}
	return nil
}

// PrepareInlineInput resolves the selected request without an execution engine.
func (r *EvalRunner) PrepareInlineInput(task *models.TestCase) (*ControlledInput, error) {
	if r == nil || r.cfg == nil {
		return nil, fmt.Errorf("inline attempt requires a configured runner")
	}
	if err := ValidateInlineConfiguration(r.cfg.Spec(), task); err != nil {
		return nil, err
	}
	request, err := r.buildExecutionRequest(task)
	if err != nil {
		return nil, err
	}
	return freezeControlledInput(request)
}

// ExecuteInlineAttempt bypasses discovery, cache and retry loops. Admission and
// lifecycle ownership stay with the caller; the request is frozen before Execute.
func (r *EvalRunner) ExecuteInlineAttempt(ctx context.Context, task *models.TestCase, input *ControlledInput, trial, attempt int) (*models.RunResult, error) {
	if r == nil || r.cfg == nil {
		return nil, fmt.Errorf("inline attempt requires a configured runner")
	}
	if err := ValidateInlineConfiguration(r.cfg.Spec(), task); err != nil {
		return nil, err
	}
	if r.cache != nil || r.skipGraders || r.snapshotWriter != nil || trial < 1 || attempt < 1 {
		return nil, fmt.Errorf("inline attempt requires one uncached graded selection without snapshot publication")
	}
	frozen, err := input.View()
	if err != nil {
		return nil, err
	}
	run := r.executeAttemptWithRequest(ctx, task, trial, attempt, frozen, true)
	return &run, nil
}
