package graders

import (
	"context"
	"fmt"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
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
}

// Create creates a validator from the global registry
func Create(graderType models.GraderKind, identifier string, params map[string]any) (Grader, error) {
	switch graderType {
	case models.GraderKindInlineScript:
		var v *struct {
			Assertions []string
			Language   Language
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		if v.Language == "" {
			v.Language = LanguagePython
		}

		return NewInlineScriptGrader(identifier, v.Language, v.Assertions)
	case models.GraderKindRegex:
		var v *struct {
			MustMatch    []string `mapstructure:"must_match"`
			MustNotMatch []string `mapstructure:"must_not_match"`
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewRegexGrader(identifier, v.MustMatch, v.MustNotMatch)
	case models.GraderKindFile:
		var v *struct {
			MustExist       []string `mapstructure:"must_exist"`
			MustNotExist    []string `mapstructure:"must_not_exist"`
			ContentPatterns []struct {
				Path         string   `mapstructure:"path"`
				MustMatch    []string `mapstructure:"must_match"`
				MustNotMatch []string `mapstructure:"must_not_match"`
			} `mapstructure:"content_patterns"`
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		var contentPatterns []FileContentPattern
		for _, cp := range v.ContentPatterns {
			contentPatterns = append(contentPatterns, FileContentPattern{
				Path:         cp.Path,
				MustMatch:    cp.MustMatch,
				MustNotMatch: cp.MustNotMatch,
			})
		}

		return NewFileGrader(FileGraderArgs{
			Name:            identifier,
			MustExist:       v.MustExist,
			MustNotExist:    v.MustNotExist,
			ContentPatterns: contentPatterns,
		})
	case models.GraderKindBehavior:
		var v BehaviorGraderParams

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewBehaviorGrader(identifier, v)
	case models.GraderKindActionSequence:
		var v ActionSequenceGraderParams

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewActionSequenceGrader(identifier, v)
	case models.GraderKindSkillInvocation:
		var v SkillInvocationGraderParams

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewSkillInvocationGrader(identifier, v)
	case models.GraderKindDiff:
		var v *struct {
			ExpectedFiles []struct {
				Path     string   `mapstructure:"path"`
				Snapshot string   `mapstructure:"snapshot"`
				Contains []string `mapstructure:"contains"`
			} `mapstructure:"expected_files"`
			ContextDir string `mapstructure:"context_dir"`
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		var expectedFiles []ExpectedFile
		for _, ef := range v.ExpectedFiles {
			expectedFiles = append(expectedFiles, ExpectedFile{
				Path:     ef.Path,
				Snapshot: ef.Snapshot,
				Contains: ef.Contains,
			})
		}

		return NewDiffGrader(DiffGraderArgs{
			Name:          identifier,
			ExpectedFiles: expectedFiles,
			ContextDir:    v.ContextDir,
		})
	case models.GraderKindPrompt, models.GraderKindKeyword, models.GraderKindJSONSchema, models.GraderKindProgram:
		return nil, fmt.Errorf("'%s' is not yet implemented", graderType)
	default:
		return nil, fmt.Errorf("'%s' is not a valid grader type", graderType)
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
