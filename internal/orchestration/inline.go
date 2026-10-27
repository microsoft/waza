package orchestration

import (
	"context"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
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
	if err := validateSingleTurnTextConfiguration(spec, task); err != nil {
		return err
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
	frozen, err := r.prepareInlineAttempt(task)
	if err != nil {
		return nil, err
	}
	previous := r.evalRunID
	r.evalRunID = origin.EvalID
	defer func() { r.evalRunID = previous }()
	run := r.executeAttemptWithRequest(ctx, task, origin.RunNumber, origin.AttemptCount, frozen, true)
	return &run, nil
}

// adaptLocalUnit retains the local benchmark's identity, lifecycle, result
// family and error policy. Only an uncached single trial/attempt is mapped;
// discovery, batching and retries remain owned by the historical orchestrator.
func (r *EvalRunner) adaptLocalUnit(ctx context.Context, task *models.TestCase, runNum, attempt int) (models.RunResult, bool) {
	spec := r.cfg.Spec()
	if !r.singleUnit || spec.Config.EngineType != "mock" || r.cache != nil || r.skipGraders || r.snapshotWriter != nil ||
		spec.TasksFrom != "" || spec.Adversarial != nil || len(spec.Inputs) != 0 ||
		len(spec.Metrics) != 0 || len(task.Requirements) != 0 ||
		len(task.Stimulus.Metadata) != 0 ||
		task.ContextRoot != "" || task.Stimulus.WorkDir != "" ||
		len(spec.Config.InstructionFiles) != 0 || len(task.InstructionFiles) != 0 ||
		ValidateInlineConfiguration(spec, task) != nil {
		return models.RunResult{}, false
	}
	for _, resource := range task.Stimulus.Resources {
		if resource.Body == "" {
			return models.RunResult{}, false
		}
	}
	frozen, err := r.prepareInlineAttempt(task)
	if err != nil {
		return models.RunResult{}, false
	}
	return r.executeAttemptWithRequest(ctx, task, runNum, attempt, frozen, false), true
}

// Both public and local adapters resolve and freeze the same implemented
// single-turn request boundary. Local evidence is not relabeled as runner JSON.
func (r *EvalRunner) prepareInlineAttempt(task *models.TestCase) (*execution.ExecutionRequest, error) {
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
	return frozen, nil
}
