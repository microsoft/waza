package models

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// GraderParameters is a polymorphic grader config payload decoded from YAML based on GraderKind.
type GraderParameters interface {
	isGraderParameters()
}

// GenericGraderParameters is used for unknown kinds to preserve raw config values.
type GenericGraderParameters map[string]any

func (GenericGraderParameters) isGraderParameters() {}

type Language string

const (
	LanguagePython     Language = "python"
	LanguageJavascript Language = "javascript"
)

type InlineScriptGraderParameters struct {
	Assertions []string `yaml:"assertions,omitempty" json:"assertions,omitempty"`

	// Language indicates which language the Assertions are written for. Defaults to [LanguagePython]
	Language Language `yaml:"language,omitempty" json:"language,omitempty"`
}

func (InlineScriptGraderParameters) isGraderParameters() {}

// TextGraderParameters holds the arguments for creating a text grader.
type TextGraderParameters struct {
	// Contains lists substrings that must appear in the output (case-insensitive).
	Contains []string `yaml:"contains,omitempty" json:"contains,omitempty"`

	// NotContains lists substrings that must NOT appear in the output (case-insensitive).
	NotContains []string `yaml:"not_contains,omitempty" json:"not_contains,omitempty"`

	// ContainsCS lists substrings that must appear in the output (case-sensitive).
	ContainsCS []string `yaml:"contains_cs,omitempty" json:"contains_cs,omitempty"`

	// NotContainsCS lists substrings that must NOT appear in the output (case-sensitive).
	NotContainsCS []string `yaml:"not_contains_cs,omitempty" json:"not_contains_cs,omitempty"`

	// RegexMatch lists regex patterns that must match somewhere in the output.
	RegexMatch []string `yaml:"regex_match,omitempty" json:"regex_match,omitempty"`

	// RegexNotMatch lists regex patterns that must NOT match anywhere in the output.
	RegexNotMatch []string `yaml:"regex_not_match,omitempty" json:"regex_not_match,omitempty"`
}

func (TextGraderParameters) isGraderParameters() {}

type FileContentPatternParameters struct {
	Path         string   `yaml:"path" json:"path"`
	MustMatch    []string `yaml:"must_match,omitempty" json:"must_match,omitempty"`
	MustNotMatch []string `yaml:"must_not_match,omitempty" json:"must_not_match,omitempty"`
}

type FileGraderParameters struct {
	MustExist       []string                       `yaml:"must_exist,omitempty" json:"must_exist,omitempty"`
	MustNotExist    []string                       `yaml:"must_not_exist,omitempty" json:"must_not_exist,omitempty"`
	ContentPatterns []FileContentPatternParameters `yaml:"content_patterns,omitempty" json:"content_patterns,omitempty"`
}

func (FileGraderParameters) isGraderParameters() {}

type BehaviorGraderParameters struct {
	MaxToolCalls   int      `yaml:"max_tool_calls,omitempty" json:"max_tool_calls,omitempty"`
	MaxTokens      int      `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	RequiredTools  []string `yaml:"required_tools,omitempty" json:"required_tools,omitempty"`
	ForbiddenTools []string `yaml:"forbidden_tools,omitempty" json:"forbidden_tools,omitempty"`
	MaxDurationMS  int64    `yaml:"max_duration_ms,omitempty" json:"max_duration_ms,omitempty"`
}

func (BehaviorGraderParameters) isGraderParameters() {}

type ActionSequenceGraderParameters struct {
	MatchingMode    ActionSequenceMatchingMode `yaml:"matching_mode,omitempty" json:"matching_mode,omitempty"`
	ExpectedActions []string                   `yaml:"expected_actions,omitempty" json:"expected_actions,omitempty"`
}

func (ActionSequenceGraderParameters) isGraderParameters() {}

// ActionSequenceMatchingMode controls how actual tool calls are compared to expected actions.
type ActionSequenceMatchingMode string

const (
	ActionSequenceMatchingModeExact    ActionSequenceMatchingMode = "exact_match"
	ActionSequenceMatchingModeInOrder  ActionSequenceMatchingMode = "in_order_match"
	ActionSequenceMatchingModeAnyOrder ActionSequenceMatchingMode = "any_order_match"
)

type SkillInvocationGraderParameters struct {
	RequiredSkills []string                    `yaml:"required_skills,omitempty" json:"required_skills,omitempty"`
	Mode           SkillInvocationMatchingMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	AllowExtra     *bool                       `yaml:"allow_extra,omitempty" json:"allow_extra,omitempty"`
}

// SkillInvocationMatchingMode controls how actual skill invocations are compared to expected skills.
type SkillInvocationMatchingMode string

const (
	SkillMatchingModeExact    SkillInvocationMatchingMode = "exact_match"
	SkillMatchingModeInOrder  SkillInvocationMatchingMode = "in_order"
	SkillMatchingModeAnyOrder SkillInvocationMatchingMode = "any_order"
)

func (SkillInvocationGraderParameters) isGraderParameters() {}

type ToolSpecParameters struct {
	Tool           string `yaml:"tool" json:"tool"`
	CommandPattern string `yaml:"command_pattern,omitempty" json:"command_pattern,omitempty"`
	SkillPattern   string `yaml:"skill_pattern,omitempty" json:"skill_pattern,omitempty"`
	PathPattern    string `yaml:"path_pattern,omitempty" json:"path_pattern,omitempty"`
}

type ToolConstraintGraderParameters struct {
	ExpectTools []ToolSpecParameters `yaml:"expect_tools,omitempty" json:"expect_tools,omitempty"`
	RejectTools []ToolSpecParameters `yaml:"reject_tools,omitempty" json:"reject_tools,omitempty"`
}

func (ToolConstraintGraderParameters) isGraderParameters() {}

// DiffExpectedFileParameters defines a single file expectation for the diff grader.
// Either Snapshot or Contains (or both) must be specified.
type DiffExpectedFileParameters struct {
	// Path is the workspace-relative path to the file being checked.
	Path string `yaml:"path" json:"path"`

	// Snapshot is the path (relative to context/fixtures dir) of the expected file content.
	// When set, the workspace file must match this snapshot exactly.
	Snapshot string `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`

	// Contains lists line fragments that must appear in the workspace file.
	// Prefixed with "+" means the line must be present; "-" means it must be absent.
	Contains []string `yaml:"contains,omitempty" json:"contains,omitempty"`
}

type DiffGraderParameters struct {
	ExpectedFiles   []DiffExpectedFileParameters `yaml:"expected_files,omitempty" json:"expected_files,omitempty"`
	ContextDir      string                       `yaml:"context_dir,omitempty" json:"context_dir,omitempty"`
	UpdateSnapshots bool                         `yaml:"update_snapshots,omitempty" json:"update_snapshots,omitempty"`
}

func (DiffGraderParameters) isGraderParameters() {}

type PromptGraderMode string

const (
	PromptGraderModeIndependent PromptGraderMode = "independent"
	PromptGraderModePairwise    PromptGraderMode = "pairwise"
)

type PromptGraderParameters struct {
	Prompt          string           `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Model           string           `yaml:"model,omitempty" json:"model,omitempty"`
	ContinueSession bool             `yaml:"continue_session,omitempty" json:"continue_session,omitempty"`
	Mode            PromptGraderMode `yaml:"mode,omitempty" json:"mode,omitempty"`
}

func (PromptGraderParameters) isGraderParameters() {}

// JSONSchemaGraderParameters holds the arguments for creating a JSON schema grader.
type JSONSchemaGraderParameters struct {
	// Schema is an inline JSON schema object used for validation.
	Schema map[string]any `yaml:"schema,omitempty" json:"schema,omitempty"`

	// SchemaFile is a path to a JSON schema file. Used when Schema is not provided.
	SchemaFile string `yaml:"schema_file,omitempty" json:"schema_file,omitempty"`
}

func (JSONSchemaGraderParameters) isGraderParameters() {}

type ProgramGraderParameters struct {
	Command string   `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	Timeout int      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

func (ProgramGraderParameters) isGraderParameters() {}

// TriggerHeuristicGraderParameters holds the arguments for creating a trigger heuristic grader.
type TriggerHeuristicGraderParameters struct {
	SkillPath string   `yaml:"skill_path" json:"skill_path"`
	Mode      string   `yaml:"mode" json:"mode"`
	Threshold *float64 `yaml:"threshold,omitempty" json:"threshold,omitempty"`
}

func (TriggerHeuristicGraderParameters) isGraderParameters() {}

func decodeGraderParameters(kind GraderKind, configNode *yaml.Node) (GraderParameters, error) {
	switch kind {
	case GraderKindInlineScript:
		return decodeYAMLNode[InlineScriptGraderParameters](configNode)
	case GraderKindText:
		return decodeYAMLNode[TextGraderParameters](configNode)
	case GraderKindFile:
		return decodeYAMLNode[FileGraderParameters](configNode)
	case GraderKindBehavior:
		return decodeYAMLNode[BehaviorGraderParameters](configNode)
	case GraderKindActionSequence:
		return decodeYAMLNode[ActionSequenceGraderParameters](configNode)
	case GraderKindSkillInvocation:
		return decodeYAMLNode[SkillInvocationGraderParameters](configNode)
	case GraderKindToolConstraint:
		return decodeYAMLNode[ToolConstraintGraderParameters](configNode)
	case GraderKindDiff:
		return decodeYAMLNode[DiffGraderParameters](configNode)
	case GraderKindPrompt:
		return decodeYAMLNode[PromptGraderParameters](configNode)
	case GraderKindJSONSchema:
		return decodeYAMLNode[JSONSchemaGraderParameters](configNode)
	case GraderKindProgram:
		return decodeYAMLNode[ProgramGraderParameters](configNode)
	case GraderKindTrigger:
		return decodeYAMLNode[TriggerHeuristicGraderParameters](configNode)
	default:
		return decodeYAMLNode[GenericGraderParameters](configNode)
	}
}

func decodeYAMLNode[T GraderParameters](node *yaml.Node) (T, error) {
	var target T

	if node == nil ||
		node.Kind == 0 { // apparently you can get an empty YAML node
		return target, nil
	}

	if err := node.Decode(&target); err != nil {
		return target, fmt.Errorf("failed to decode grader config of type %T: %w", target, err)
	}

	return target, nil
}
