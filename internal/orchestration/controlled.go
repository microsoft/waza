package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// ControlledObservation is not serialized as a legacy RunResult. The caller
// durably allocates Origin before invocation and retains every returned attempt.
type ControlledObservation struct {
	Origin models.EvidenceOrigin
	Run    models.RunResult
	Checks []ControlledCheck
}

type ControlledCheck struct {
	Scope  string
	Result models.GraderResults
}

// ValidateControlledExecution limits affirmative operational observability to
// the instrumented mock/text path. In particular, program graders' nil-error
// launch/timeout rejection must never be inferred to be a behavioral outcome.
// Unsupported strict paths do not change ordinary evaluation capabilities.
func (r *EvalRunner) ValidateControlledExecution(tc *models.TestCase) error {
	if r == nil || r.cfg == nil || r.cfg.Spec() == nil || tc == nil {
		return fmt.Errorf("controlled execution requires an eval configuration and task")
	}
	if _, ok := r.engine.(*execution.MockEngine); !ok {
		return fmt.Errorf("controlled operational observability is unavailable for this engine; ordinary run remains supported")
	}
	return ValidateControlledConfiguration(r.cfg.Spec(), tc)
}

// ValidateControlledConfiguration is static capability validation, not runtime
// readiness or observed assurance. It does not construct/initialize an engine.
func ValidateControlledConfiguration(spec *models.EvalSpec, tc *models.TestCase) error {
	if spec == nil || tc == nil {
		return fmt.Errorf("controlled configuration requires a spec and task")
	}
	if spec.Config.EngineType != "mock" || !spec.SkillsDisabledForTask(tc.SkillPaths) {
		return fmt.Errorf("controlled mock execution requires actual mock configuration with ambient skills disabled")
	}
	if spec.Baseline || spec.Config.Concurrent ||
		!reflect.ValueOf(spec.Hooks).IsZero() || len(spec.MCPMocks) != 0 ||
		len(spec.Config.ServerConfigs) != 0 || len(spec.CommandMocks) != 0 ||
		tc.CommandMocks != nil || len(tc.Checkpoints) != 0 || tc.Stimulus.Responder != nil ||
		len(tc.Stimulus.FollowUps) != 0 || len(tc.Stimulus.Repos) != 0 {
		return fmt.Errorf("controlled execution cannot certify selected hooks/cache/parallel/dependency/multiturn/repository modes")
	}
	if spec.SkillName != "" || len(spec.Config.SkillPaths) != 0 || len(tc.SkillPaths) != 0 {
		return fmt.Errorf("controlled execution does not yet bind discovered skill/agent definitions")
	}
	seen := map[string]bool{}
	for _, g := range spec.Graders {
		_, nativeText := g.Parameters.(models.TextGraderParameters)
		if g.Kind != models.GraderKindText || !nativeText || seen[g.Identifier] || g.Identifier == "" ||
			g.Ref != "" || g.ScriptPath != "" || g.Rubric != "" {
			return fmt.Errorf("controlled grading requires unique independently attributable builtin text graders")
		}
		seen[g.Identifier] = true
	}
	for _, g := range tc.Validators {
		_, nativeText := g.Parameters.(models.TextGraderParameters)
		if g.Kind != models.GraderKindText || !nativeText || seen[g.Identifier] || g.Identifier == "" {
			return fmt.Errorf("controlled grading cannot certify unsupported or colliding grader results")
		}
		seen[g.Identifier] = true
	}
	exp := tc.Expectation
	if (len(exp.MustInclude) > 0 && seen["_output_contains"]) ||
		(len(exp.MustExclude) > 0 && seen["_output_not_contains"]) ||
		(len(exp.MayInclude) > 0 && seen["_output_contains_any"]) {
		return fmt.Errorf("controlled grading cannot attribute colliding expectation result names")
	}
	if len(exp.OutcomeSpecs) != 0 || len(exp.ToolPatterns) != 0 || !reflect.ValueOf(exp.BehaviorRules).IsZero() ||
		exp.ExpectedTrigger != nil {
		return fmt.Errorf("controlled execution does not yet certify these expectation types")
	}
	if len(seen) == 0 && len(exp.MustInclude)+len(exp.MustExclude)+len(exp.MayInclude) == 0 {
		return fmt.Errorf("controlled execution requires at least one observable check")
	}
	return nil
}

// ExecuteControlledAttempt is sequential and uncached. Engine initialization
// and fresh-engine/workspace ownership remain with the paired collector. It
// never calls Bind to replace old provenance, and sets the attempt before any
// snapshot capture, unlike legacy final-attempt-only accounting.
func (r *EvalRunner) ExecuteControlledAttempt(ctx context.Context, tc *models.TestCase, origin models.EvidenceOrigin) (*ControlledObservation, error) {
	return r.executeControlledAttempt(ctx, tc, nil, origin)
}

// PrepareControlledInput resolves the exact read-only input later passed to
// execution. Unlike legacy resource loading, missing declared files are errors.
func (r *EvalRunner) PrepareControlledInput(tc *models.TestCase) (*ControlledInput, error) {
	if r == nil || r.cfg == nil {
		return nil, fmt.Errorf("controlled input needs configuration")
	}
	if err := ValidateControlledConfiguration(r.cfg.Spec(), tc); err != nil {
		return nil, err
	}
	for _, ref := range tc.Stimulus.Resources {
		if ref.Body != "" {
			continue
		}
		base := r.cfg.FixtureDir()
		if tc.ContextRoot != "" {
			base = tc.ContextRoot
		}
		_, path, err := resolveContextFile(base, ref.Location, "controlled resource")
		if err != nil {
			return nil, err
		}
		if _, err := os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("reading required controlled resource: %w", err)
		}
	}
	request, err := r.buildExecutionRequest(tc)
	if err != nil {
		return nil, err
	}
	return freezeControlledInput(request)
}

// ExecutePreparedControlledAttempt consumes the same immutable request whose
// source bytes were bound before collection, avoiding a second fixture read.
func (r *EvalRunner) ExecutePreparedControlledAttempt(ctx context.Context, tc *models.TestCase, input *ControlledInput, origin models.EvidenceOrigin) (*ControlledObservation, error) {
	if input == nil {
		return nil, fmt.Errorf("controlled execution needs a fresh prepared request")
	}
	request, err := input.View()
	if err != nil {
		return nil, err
	}
	return r.executeControlledAttempt(ctx, tc, request, origin)
}

// ControlledInput owns private source bytes. View returns an independent deep
// copy, never the committed request or engine-mutated attempt request.
type ControlledInput struct {
	data []byte
}

func freezeControlledInput(req *execution.ExecutionRequest) (*ControlledInput, error) {
	if req == nil || req.SessionID != "" || req.WorkspaceDir != "" || req.PermissionHandler != nil ||
		len(req.Tools) != 0 || req.ToolPolicy != nil || len(req.GitResources) != 0 ||
		len(req.MCPServers) != 0 || len(req.CommandMocks) != 0 || len(req.SkillPaths) != 0 {
		return nil, fmt.Errorf("controlled request contains unsupported session/dependency capabilities")
	}
	// Function-typed SDK members cannot be JSON encoded, even when nil. Remove
	// only those known absent members; all remaining request fields are bound.
	type request execution.ExecutionRequest
	value := struct {
		*request
		PermissionHandler any `json:"PermissionHandler"`
		Tools             any `json:"Tools"`
	}{request: (*request)(req)}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("freezing controlled request: %w", err)
	}
	return &ControlledInput{data: data}, nil
}

func (input *ControlledInput) View() (*execution.ExecutionRequest, error) {
	if input == nil {
		return nil, fmt.Errorf("missing frozen controlled input")
	}
	var req execution.ExecutionRequest
	if err := json.Unmarshal(input.data, &req); err != nil {
		return nil, fmt.Errorf("decoding frozen controlled request: %w", err)
	}
	return &req, nil
}

func (r *EvalRunner) executeControlledAttempt(ctx context.Context, tc *models.TestCase, prepared *execution.ExecutionRequest, origin models.EvidenceOrigin) (*ControlledObservation, error) {
	if origin.EvalID == "" || tc == nil || origin.TaskID != tc.TestID ||
		origin.RunNumber < 1 || origin.AttemptCount < 1 || origin.PriorAttempts != "" {
		return nil, fmt.Errorf("controlled attempt requires its complete preallocated origin")
	}
	if err := r.ValidateControlledExecution(tc); err != nil {
		return nil, err
	}
	if r.cache != nil || r.skipGraders {
		return nil, fmt.Errorf("controlled attempt cannot use cached or ungraded execution")
	}
	previous := r.evalRunID
	r.evalRunID = origin.EvalID
	defer func() { r.evalRunID = previous }()
	run := r.executeRunWithAttempt(ctx, tc, origin.RunNumber, origin.AttemptCount, prepared)
	checks := []ControlledCheck{}
	names := make([]string, 0, len(run.Validations))
	for name := range run.Validations {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		scope := "expectation"
		for _, grader := range r.cfg.Spec().Graders {
			if grader.Identifier == name {
				scope = "eval"
			}
		}
		for _, grader := range tc.Validators {
			if grader.Identifier == name {
				scope = "task"
			}
		}
		checks = append(checks, ControlledCheck{Scope: scope, Result: run.Validations[name]})
	}
	return &ControlledObservation{Origin: origin, Run: run, Checks: checks}, nil
}
