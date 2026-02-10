package graders

import (
	"context"
	"fmt"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spboyer/waza/internal/models"
)

type Type string

const (
	// this was originally in the other prototype, and just eval'd. We don't need to
	// embed a Python interpreter here, so reconsider how we want to do that one.
	TypeInlineScript Type = "code"

	TypePrompt Type = "prompt"
	TypeRegex  Type = "regex"

	// TypeFile does existence/content checks
	TypeFile       Type = "file"
	TypeKeyword    Type = "keyword"
	TypeJSONSchema Type = "json_schema"

	TypeProgram Type = "program"

	// TODO: unsure what this would actually be.
	// TypeComposite Type = "composite"
)

// Grader is the interface for all validators
type Grader interface {
	// Identifier returns the validator name
	Name() string

	// Category returns the validator type
	Type() Type

	// Validate performs validation and returns a result
	Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error)
}

// Context provides context for validation
type Context struct {
	TestCase   *models.TestCase
	Transcript []models.TranscriptEntry
	Output     string
	Outcome    map[string]any
	DurationMS int64
	Metadata   map[string]any

	// WorkspaceDir is the sandbox folder we used for this session - it should contain any edits
	// or other changes we've made. This can be useful for things like the [FileGrader],
	// where you want to verify artifacts or outputs.
	WorkspaceDir string
}

// Create creates a validator from the global registry
func Create(graderType Type, identifier string, params map[string]any) (Grader, error) {
	switch graderType {
	case TypeInlineScript:
		var v *struct {
			Assertions []string
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewInlineScriptGrader(identifier, LanguagePython, v.Assertions)
	case TypeRegex:
		var v *struct {
			MustMatch    []string `mapstructure:"must_match"`
			MustNotMatch []string `mapstructure:"must_not_match"`
		}

		if err := mapstructure.Decode(params, &v); err != nil {
			return nil, err
		}

		return NewRegexGrader(identifier, v.MustMatch, v.MustNotMatch)
	case TypeFile:
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
	case TypePrompt, TypeKeyword, TypeJSONSchema, TypeProgram:
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
