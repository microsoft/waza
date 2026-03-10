package graders

import (
	"context"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// Grader is the interface for all validators
type Grader interface {
	// Identifier returns the validator name
	Name() string

	// Category returns the validator type
	Kind() models.GraderKind

	// Validate performs validation and returns a result
	Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error)
}

// Context provides context for validation
type Context struct {
	TestCase   *models.TestCase
	Transcript []models.TranscriptEvent
	Output     string
	Outcome    map[string]any
	DurationMS int64
	Metadata   map[string]any

	// WorkspaceDir is the sandbox folder we used for this session - it should contain any edits
	// or other changes we've made. This can be useful for things like the [FileGrader],
	// where you want to verify artifacts or outputs.
	WorkspaceDir string

	// Session holds the session digest with tool call counts, token usage, and tools used.
	// Used by the behavior grader to validate agent behavior constraints.
	Session *models.SessionDigest

	// SkillInvocations is a chronological list of skills invoked during the session.
	// Used by the skill_invocation grader to verify orchestration workflows.
	SkillInvocations []execution.SkillInvocation

	// SessionID from this evaluation run.
	SessionID string

	// BaselineOutput is the agent output from the baseline (no-skill) run.
	// Populated when running in baseline mode; used by pairwise prompt grading.
	BaselineOutput string
}

// Create creates a validator from the global registry
func Create(identifier string, params models.GraderParameters) (Grader, error) {
	switch p := params.(type) {
	case models.InlineScriptGraderParameters:
		return NewInlineScriptGrader(identifier, models.Language(p.Language), p.Assertions)
	case models.TextGraderParameters:
		return NewTextGrader(identifier, p)
	case models.FileGraderParameters:
		return NewFileGrader(identifier, p)
	case models.BehaviorGraderParameters:
		return NewBehaviorGrader(identifier, p)
	case models.ActionSequenceGraderParameters:
		return NewActionSequenceGrader(identifier, p)
	case models.SkillInvocationGraderParameters:
		return NewSkillInvocationGrader(identifier, p)
	case models.ToolConstraintGraderParameters:
		return NewToolConstraintGrader(identifier, p)
	case models.DiffGraderParameters:
		return NewDiffGrader(identifier, p)
	case models.PromptGraderParameters:
		return NewPromptGrader(identifier, p)
	case models.JSONSchemaGraderParameters:
		return NewJSONSchemaGrader(identifier, p)
	case models.ProgramGraderParameters:
		return NewProgramGrader(identifier, p)
	default:
		return nil, fmt.Errorf("'%T' is not a valid grader configuration", params)
	}
}

func paramsAsInlineScript(params models.GraderParameters) (models.InlineScriptGraderParameters, error) {
	if params == nil {
		return models.InlineScriptGraderParameters{}, nil
	}

	v, ok := params.(models.InlineScriptGraderParameters)
	if !ok {
		return models.InlineScriptGraderParameters{}, unexpectedParamsType(models.GraderKindInlineScript, params, "models.InlineScriptGraderParameters")
	}

	return v, nil
}

func paramsAsText(params models.GraderParameters) (models.TextGraderParameters, error) {
	if params == nil {
		return models.TextGraderParameters{}, nil
	}

	v, ok := params.(models.TextGraderParameters)
	if !ok {
		return models.TextGraderParameters{}, unexpectedParamsType(models.GraderKindText, params, "models.TextGraderParameters")
	}

	return v, nil
}

func paramsAsFile(params models.GraderParameters) (models.FileGraderParameters, error) {
	if params == nil {
		return models.FileGraderParameters{}, nil
	}

	v, ok := params.(models.FileGraderParameters)
	if !ok {
		return models.FileGraderParameters{}, unexpectedParamsType(models.GraderKindFile, params, "models.FileGraderParameters")
	}

	return v, nil
}

func paramsAsBehavior(params models.GraderParameters) (models.BehaviorGraderParameters, error) {
	if params == nil {
		return models.BehaviorGraderParameters{}, nil
	}

	v, ok := params.(models.BehaviorGraderParameters)
	if !ok {
		return models.BehaviorGraderParameters{}, unexpectedParamsType(models.GraderKindBehavior, params, "models.BehaviorGraderParameters")
	}

	return v, nil
}

func paramsAsActionSequence(params models.GraderParameters) (models.ActionSequenceGraderParameters, error) {
	if params == nil {
		return models.ActionSequenceGraderParameters{}, nil
	}

	v, ok := params.(models.ActionSequenceGraderParameters)
	if !ok {
		return models.ActionSequenceGraderParameters{}, unexpectedParamsType(models.GraderKindActionSequence, params, "models.ActionSequenceGraderParameters")
	}

	return v, nil
}

func paramsAsSkillInvocation(params models.GraderParameters) (models.SkillInvocationGraderParameters, error) {
	if params == nil {
		return models.SkillInvocationGraderParameters{}, nil
	}

	v, ok := params.(models.SkillInvocationGraderParameters)
	if !ok {
		return models.SkillInvocationGraderParameters{}, unexpectedParamsType(models.GraderKindSkillInvocation, params, "models.SkillInvocationGraderParameters")
	}

	return v, nil
}

func paramsAsToolConstraint(params models.GraderParameters) (models.ToolConstraintGraderParameters, error) {
	if params == nil {
		return models.ToolConstraintGraderParameters{}, nil
	}

	v, ok := params.(models.ToolConstraintGraderParameters)
	if !ok {
		return models.ToolConstraintGraderParameters{}, unexpectedParamsType(models.GraderKindToolConstraint, params, "models.ToolConstraintGraderParameters")
	}

	return v, nil
}

func paramsAsDiff(params models.GraderParameters) (models.DiffGraderParameters, error) {
	if params == nil {
		return models.DiffGraderParameters{}, nil
	}

	v, ok := params.(models.DiffGraderParameters)
	if !ok {
		return models.DiffGraderParameters{}, unexpectedParamsType(models.GraderKindDiff, params, "models.DiffGraderParameters")
	}

	return v, nil
}

func paramsAsPrompt(params models.GraderParameters) (models.PromptGraderParameters, error) {
	if params == nil {
		return models.PromptGraderParameters{}, nil
	}

	v, ok := params.(models.PromptGraderParameters)
	if !ok {
		return models.PromptGraderParameters{}, unexpectedParamsType(models.GraderKindPrompt, params, "models.PromptGraderParameters")
	}

	return v, nil
}

func paramsAsJSONSchema(params models.GraderParameters) (models.JSONSchemaGraderParameters, error) {
	if params == nil {
		return models.JSONSchemaGraderParameters{}, nil
	}

	v, ok := params.(models.JSONSchemaGraderParameters)
	if !ok {
		return models.JSONSchemaGraderParameters{}, unexpectedParamsType(models.GraderKindJSONSchema, params, "models.JSONSchemaGraderParameters")
	}

	return v, nil
}

func paramsAsProgram(params models.GraderParameters) (models.ProgramGraderParameters, error) {
	if params == nil {
		return models.ProgramGraderParameters{}, nil
	}

	v, ok := params.(models.ProgramGraderParameters)
	if !ok {
		return models.ProgramGraderParameters{}, unexpectedParamsType(models.GraderKindProgram, params, "models.ProgramGraderParameters")
	}

	return v, nil
}

func unexpectedParamsType(kind models.GraderKind, params models.GraderParameters, expected string) error {
	return fmt.Errorf("grader type %q received config of type %T; expected %s", kind, params, expected)
}

// measureTime is a helper to measure validation duration
func measureTime(fn func() (*models.GraderResults, error)) (*models.GraderResults, error) {
	start := time.Now()
	result, err := fn()

	if result != nil {
		result.DurationMs = time.Since(start).Milliseconds()
	}

	return result, err
}
