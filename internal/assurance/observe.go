// Package assurance observes existing graders without changing their evaluation
// semantics. Observations alone do not establish reviewed-label agreement or
// evidence completeness.
package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type ObservationState string

const (
	Observed             ObservationState = "observed"
	NotAssessed          ObservationState = "not_assessed"
	InsufficientEvidence ObservationState = "insufficient_evidence"
	OperationalError     ObservationState = "operational_error"
	Invalid              ObservationState = "invalid"
)

// ReferenceInput is an in-memory reference, not a serialized evidence manifest.
// Context must contain evaluator-owned evidence, never an agent's live workspace.
type ReferenceInput struct {
	TaskID     string
	Check      models.RequirementCheck
	Parameters models.GraderParameters
	Context    *graders.Context
}

// Observation preserves a scoped declaration's actual result. Observed is not
// an assurance pass: reviewed labels and complete evidence are separate gates.
type Observation struct {
	TaskID string
	Check  models.RequirementCheck
	State  ObservationState
	Result *models.GraderResults
	Err    error
}

// ObserveDeclaredMechanical resolves the native declaration through preflight's
// shared pure lookup. It does not interpret static resolution as enforcement.
func ObserveDeclaredMechanical(ctx context.Context, task *models.TestCase, spec *models.EvalSpec, check models.RequirementCheck, evidence *graders.Context) Observation {
	if task == nil || spec == nil {
		return Observation{Check: check, State: Invalid, Err: errors.New("reference declaration requires a task and eval spec")}
	}
	declaration, err := preflight.ResolveGrader(check, task, spec)
	if err != nil {
		return Observation{TaskID: task.TestID, Check: check, State: Invalid, Err: fmt.Errorf("resolving reference declaration: %w", err)}
	}
	input := ReferenceInput{TaskID: task.TestID, Check: check, Context: evidence}
	if declaration.Config != nil {
		input.Parameters = declaration.Config.Parameters
	} else if declaration.Inline != nil {
		input.Parameters = declaration.Inline.Parameters
	} else {
		return Observation{TaskID: task.TestID, Check: check, State: OperationalError, Err: errors.New("resolved reference has no grader declaration")}
	}
	return ObserveMechanical(ctx, input)
}

// ObserveMechanical never starts an agent, judge, or external grader process.
// Graders with uninstrumented operational failures are explicitly not assessed.
func ObserveMechanical(ctx context.Context, input ReferenceInput) (observation Observation) {
	observation = Observation{TaskID: input.TaskID, Check: input.Check}
	fail := func(state ObservationState, err error) Observation {
		observation.State, observation.Err = state, err
		return observation
	}
	if err := ctx.Err(); err != nil {
		return fail(OperationalError, fmt.Errorf("observing grader: %w", err))
	}
	if err := validateIdentity(input); err != nil {
		return fail(Invalid, err)
	}
	switch params := input.Parameters.(type) {
	case models.PromptGraderParameters, models.ProgramGraderParameters,
		models.InlineScriptGraderParameters, models.TriggerHeuristicGraderParameters:
		return fail(NotAssessed, fmt.Errorf("grader %q requires a separately supported execution or calibration contract", input.Check.Grader))
	case models.DiffGraderParameters:
		if params.UpdateSnapshots {
			return fail(Invalid, errors.New("reference grading cannot update snapshots"))
		}
	case models.JSONSchemaGraderParameters:
		if params.Schema == nil && params.SchemaFile != "" {
			return fail(NotAssessed, errors.New("file-backed JSON schemas require a separately confined schema loader"))
		}
	}
	if err := graders.ValidateConfig(input.Check.Grader, input.Parameters); err != nil {
		if loadErr, ok := errors.AsType[*jsonschema.LoadURLError](err); ok && errors.Is(loadErr.Err, schemaloader.ErrExternalReference) {
			return fail(NotAssessed, fmt.Errorf("validating reference grader: %w", errors.Join(schemaloader.ErrExternalReference, err)))
		}
		return fail(Invalid, fmt.Errorf("validating reference grader: %w", err))
	}
	if input.Context == nil {
		return fail(InsufficientEvidence, errors.New("reference grading context is unavailable"))
	}
	evidence, parameters, cleanup, state, err := prepareLocalEvidence(input.Parameters, input.Context)
	if err != nil {
		return fail(state, err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			observation.State = OperationalError
			observation.Err = errors.Join(observation.Err, fmt.Errorf("cleaning reference workspace: %w", err))
		}
	}()
	state, err = checkLocalPrerequisites(parameters, evidence)
	if err != nil {
		return fail(state, err)
	}
	grader, err := graders.Create(input.Check.Grader, parameters)
	if err != nil {
		return fail(Invalid, fmt.Errorf("creating reference grader: %w", err))
	}
	result, err := grader.Grade(ctx, evidence)
	observation.Result = result
	if err != nil {
		return fail(OperationalError, fmt.Errorf("grading reference: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(OperationalError, fmt.Errorf("grading reference interrupted: %w", err))
	}
	if result == nil {
		return fail(OperationalError, errors.New("reference grader returned no result"))
	}
	observation.State = Observed
	return observation
}

func validateIdentity(input ReferenceInput) error {
	if strings.TrimSpace(input.TaskID) == "" || input.TaskID != strings.TrimSpace(input.TaskID) {
		return errors.New("reference grading requires a nonempty, unpadded task ID")
	}
	check := input.Check
	if strings.TrimSpace(check.Grader) == "" || check.Grader != strings.TrimSpace(check.Grader) {
		return errors.New("reference grading requires a nonempty, unpadded grader declaration name")
	}
	switch check.Scope {
	case "eval", "task":
		if check.AfterTurn != 0 {
			return errors.New("after_turn is only valid for checkpoint scope")
		}
	case "checkpoint":
		if check.AfterTurn < 1 {
			return errors.New("checkpoint reference requires after_turn >= 1")
		}
	default:
		return fmt.Errorf("unknown reference scope %q", check.Scope)
	}
	return nil
}

func checkLocalPrerequisites(parameters models.GraderParameters, evidence *graders.Context) (ObservationState, error) {
	switch params := parameters.(type) {
	case models.BehaviorGraderParameters:
		if evidence.Session == nil {
			return InsufficientEvidence, errors.New("reference session digest is unavailable")
		}
		if params.MaxTokens > 0 && evidence.Session.Usage == nil {
			return InsufficientEvidence, errors.New("reference token usage is unavailable")
		}
	case models.ToolCallsGraderParameters, models.ToolConstraintGraderParameters:
		if evidence.Session == nil {
			return InsufficientEvidence, errors.New("reference session digest is unavailable")
		}
		if needsArgumentEvidence(parameters) {
			if err := validateArgumentEvidence(evidence); err != nil {
				return OperationalError, err
			}
		}
	case models.ActionSequenceGraderParameters:
		if evidence.Session == nil {
			return InsufficientEvidence, errors.New("reference session digest is unavailable")
		}
	case models.SkillInvocationGraderParameters:
		if evidence.SkillInvocations == nil {
			return InsufficientEvidence, errors.New("reference skill invocation evidence is unavailable")
		}
	}
	return Observed, nil
}

func needsArgumentEvidence(parameters models.GraderParameters) bool {
	switch params := parameters.(type) {
	case models.ToolCallsGraderParameters:
		for _, expectation := range params.Expect {
			if len(expectation.Args) > 0 {
				return true
			}
		}
	case models.ToolConstraintGraderParameters:
		specs := append(append([]models.ToolSpecParameters{}, params.ExpectTools...), params.RejectTools...)
		if params.AllowOnly != nil {
			specs = append(specs, (*params.AllowOnly)...)
		}
		for _, spec := range specs {
			if len(spec.Args) > 0 {
				return true
			}
		}
	}
	return false
}

func validateArgumentEvidence(evidence *graders.Context) error {
	for _, call := range evidence.Session.ToolCalls {
		if _, err := json.Marshal(call.Arguments.Extra); err != nil {
			return fmt.Errorf("normalizing reference tool %q arguments: %w", call.Name, err)
		}
	}
	for _, event := range evidence.ToolEvents {
		if event.Args == nil {
			continue
		}
		data, err := json.Marshal(event.Args)
		if err != nil {
			return fmt.Errorf("normalizing reference event %q arguments: %w", event.ToolCallID, err)
		}
		var object map[string]any
		if err := json.Unmarshal(data, &object); err != nil {
			return fmt.Errorf("reference event %q arguments must be an object: %w", event.ToolCallID, err)
		}
	}
	return nil
}
