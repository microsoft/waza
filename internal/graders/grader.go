package graders

import (
	"context"
	"fmt"
	"strings"
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
		return NewInlineScriptGrader(identifier, p)
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
	case models.TriggerHeuristicGraderParameters:
		return NewTriggerHeuristicGrader(identifier, p)
	default:
		return nil, fmt.Errorf("grader with identifier %q is using an unsupported grader type. Valid grader types: %s", identifier, strings.Join(models.AllGraderKinds(), ", "))
	}
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
