package specverify

// RequirementKind identifies the part of SKILL.md a requirement came from.
type RequirementKind string

const (
	RequirementDescription RequirementKind = "description"
	RequirementUse         RequirementKind = "use"
	RequirementDont        RequirementKind = "dont"
	RequirementParameter   RequirementKind = "parameter"
)

// SourceSpan points back to the SKILL.md source lines for a parsed requirement.
type SourceSpan struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Requirement is a machine-readable SKILL.md promise.
type Requirement struct {
	ID     string          `json:"id"`
	Kind   RequirementKind `json:"kind"`
	Text   string          `json:"text"`
	Source SourceSpan      `json:"source"`
}

// ParsedSkillSpec contains deterministic requirements extracted from SKILL.md.
type ParsedSkillSpec struct {
	SkillName    string        `json:"skill_name"`
	SkillPath    string        `json:"skill_path"`
	Requirements []Requirement `json:"requirements"`
}

// TaskRef captures the task fields needed for coverage reporting.
type TaskRef struct {
	ID              string         `json:"id"`
	Name            string         `json:"name,omitempty"`
	Path            string         `json:"path,omitempty"`
	Text            string         `json:"-"`
	ExpectedTrigger *bool          `json:"expected_trigger,omitempty"`
	Metadata        map[string]any `json:"-"`
}

// CoveredBy describes one task that exercises a requirement.
type CoveredBy struct {
	TaskID   string `json:"task_id"`
	TaskName string `json:"task_name,omitempty"`
	TaskPath string `json:"task_path,omitempty"`
	Mode     string `json:"mode"`
	Reason   string `json:"reason"`
}

// RequirementCoverage reports task coverage for one requirement.
type RequirementCoverage struct {
	Requirement Requirement `json:"requirement"`
	CoveredBy   []CoveredBy `json:"covered_by"`
}

// Summary captures aggregate verification results.
type Summary struct {
	TotalRequirements     int `json:"total_requirements"`
	CoveredRequirements   int `json:"covered_requirements"`
	UncoveredRequirements int `json:"uncovered_requirements"`
	FailThreshold         int `json:"fail_threshold,omitempty"`
}

// Report is the full machine-readable output for spec verification.
type Report struct {
	SkillPath  string                `json:"skill_path"`
	EvalPath   string                `json:"eval_path"`
	Semantic   bool                  `json:"semantic"`
	JudgeModel string                `json:"judge_model,omitempty"`
	Summary    Summary               `json:"summary"`
	Coverage   []RequirementCoverage `json:"coverage"`
}
