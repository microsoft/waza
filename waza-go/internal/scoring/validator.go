package scoring

import (
	"time"

	"github.com/spboyer/waza/waza-go/internal/models"
)

// Validator is the interface for all validators
type Validator interface {
	// Identifier returns the validator name
	Identifier() string

	// Category returns the validator type
	Category() string

	// Validate performs validation and returns a result
	Validate(ctx *ValidationContext) *models.ValidationOut
}

// ValidationContext provides context for validation
type ValidationContext struct {
	TestCase   *models.TestCase
	Transcript []models.TranscriptEntry
	Output     string
	Outcome    map[string]any
	DurationMs int64
	Metadata   map[string]any
}

// ValidatorRegistry manages validator implementations
type ValidatorRegistry struct {
	factories map[string]ValidatorFactory
}

// ValidatorFactory creates validators
type ValidatorFactory func(identifier string, params map[string]any) Validator

var globalRegistry = NewValidatorRegistry()

// NewValidatorRegistry creates a new registry
func NewValidatorRegistry() *ValidatorRegistry {
	return &ValidatorRegistry{
		factories: make(map[string]ValidatorFactory),
	}
}

// Register adds a validator factory
func (r *ValidatorRegistry) Register(category string, factory ValidatorFactory) {
	r.factories[category] = factory
}

// Create instantiates a validator
func (r *ValidatorRegistry) Create(category, identifier string, params map[string]any) Validator {
	factory, ok := r.factories[category]
	if !ok {
		return &NoOpValidator{identifier: identifier, category: category}
	}
	return factory(identifier, params)
}

// RegisterValidator adds a validator to the global registry
func RegisterValidator(category string, factory ValidatorFactory) {
	globalRegistry.Register(category, factory)
}

// CreateValidator creates a validator from the global registry
func CreateValidator(category, identifier string, params map[string]any) Validator {
	return globalRegistry.Create(category, identifier, params)
}

// NoOpValidator is a placeholder validator
type NoOpValidator struct {
	identifier string
	category   string
}

func (v *NoOpValidator) Identifier() string { return v.identifier }
func (v *NoOpValidator) Category() string   { return v.category }

func (v *NoOpValidator) Validate(ctx *ValidationContext) *models.ValidationOut {
	return &models.ValidationOut{
		Identifier: v.identifier,
		Kind:       v.category,
		Score:      0.0,
		Passed:     false,
		Feedback:   "Unknown validator category: " + v.category + " for identifier: " + v.identifier + "; no validation executed",
		DurationMs: 0,
	}
}

// measureTime is a helper to measure validation duration
func measureTime(fn func() *models.ValidationOut) *models.ValidationOut {
	start := time.Now()
	result := fn()
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}
