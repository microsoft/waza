package models

// Requirement describes intent and references existing checks. It does not
// introduce an assertion, runtime policy, or an assurance verdict.
type Requirement struct {
	ID          string             `yaml:"id" json:"id"`
	Category    string             `yaml:"category" json:"category"`
	Description string             `yaml:"description" json:"description"`
	Checks      []RequirementCheck `yaml:"checks,omitempty" json:"checks,omitempty"`
}

// RequirementCheck identifies an explicit grader declaration, not a result key.
// AfterTurn is required only for checkpoint scope.
type RequirementCheck struct {
	Scope     string `yaml:"scope" json:"scope"`
	Grader    string `yaml:"grader" json:"grader"`
	AfterTurn int    `yaml:"after_turn,omitempty" json:"after_turn,omitempty"`
}
