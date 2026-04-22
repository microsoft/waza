package models

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TestCase represents a single evaluation test
type TestCase struct {
	Active      *bool             `yaml:"enabled,omitempty" json:"active,omitempty"`
	ContextRoot string            `yaml:"context_dir,omitempty" json:"context_root,omitempty"`
	DisplayName string            `yaml:"name" json:"display_name"`
	Expectation TaskExpectation   `yaml:"expected,omitempty" json:"expectation,omitempty"`
	SkillPaths  []string          `yaml:"skill_directories,omitempty" json:"skill_paths,omitempty"`
	Stimulus    TaskStimulus      `yaml:"inputs" json:"stimulus"`
	Summary     string            `yaml:"description,omitempty" json:"summary,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"labels,omitempty"`
	TestID      string            `yaml:"id" json:"test_id"`
	TimeoutSec  *int              `yaml:"timeout_seconds,omitempty" json:"timeout_sec,omitempty"`
	Validators  []ValidatorInline `yaml:"graders,omitempty" json:"validators,omitempty"`
}

// TaskStimulus defines the input for a task.
//
// Deprecated alias: TestStimulus is provided for backward compatibility.
type TaskStimulus struct {
	Message     string            `yaml:"prompt" json:"message"`
	MessageFile string            `yaml:"prompt_file,omitempty" json:"message_file,omitempty"`
	Metadata    map[string]any    `yaml:"context,omitempty" json:"metadata,omitempty"`
	Resources   []ResourceRef     `yaml:"files,omitempty" json:"resources,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	FollowUps   []string          `yaml:"follow_up_prompts,omitempty" json:"follow_ups,omitempty"`
}

// ResourceRef points to a file or inline content
type ResourceRef struct {
	Location string `yaml:"path,omitempty" json:"location,omitempty"`
	Body     string `yaml:"content,omitempty" json:"body,omitempty"`
}

// TaskExpectation defines expected outcomes.
//
// Deprecated alias: TestExpectation is provided for backward compatibility.
type TaskExpectation struct {
	OutcomeSpecs    []OutcomeSpec  `yaml:"outcomes,omitempty" json:"outcome_specs,omitempty"`
	ToolPatterns    map[string]any `yaml:"tool_calls,omitempty" json:"tool_patterns,omitempty"`
	BehaviorRules   BehaviorRules  `yaml:"behavior,omitempty" json:"behavior_rules,omitempty"`
	MustInclude     []string       `yaml:"output_contains,omitempty" json:"must_include,omitempty"`
	MustExclude     []string       `yaml:"output_not_contains,omitempty" json:"must_exclude,omitempty"`
	MayInclude      []string       `yaml:"output_contains_any,omitempty" json:"may_include,omitempty"`
	ExpectedTrigger *bool          `yaml:"should_trigger,omitempty" json:"expected_trigger,omitempty"`
}

type OutcomeSpec struct {
	Category  string `yaml:"type" json:"category"`
	Value     any    `yaml:"value,omitempty" json:"value,omitempty"`
	Predicate string `yaml:"condition,omitempty" json:"predicate,omitempty"`
}

type BehaviorRules struct {
	MaxToolInvocations int      `yaml:"max_tool_calls,omitempty" json:"max_tool_invocations,omitempty"`
	MaxRounds          int      `yaml:"max_iterations,omitempty" json:"max_rounds,omitempty"`
	MaxTokens          int      `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	MaxResponseTimeMs  int64    `yaml:"max_response_time_ms,omitempty" json:"max_response_time_ms,omitempty"`
	MustUseTool        []string `yaml:"required_tools,omitempty" json:"must_use_tool,omitempty"`
	ForbidTool         []string `yaml:"forbidden_tools,omitempty" json:"forbid_tool,omitempty"`
}

// ValidatorInline is a validator embedded in a test case
type ValidatorInline struct {
	Identifier string           `yaml:"name" json:"identifier"`
	Kind       GraderKind       `yaml:"type,omitempty" json:"kind,omitempty"`
	Checks     []string         `yaml:"assertions,omitempty" json:"checks,omitempty"`
	Rubric     string           `yaml:"rubric,omitempty" json:"rubric,omitempty"`
	Weight     float64          `yaml:"weight,omitempty" json:"weight,omitempty"`
	Parameters GraderParameters `yaml:"config,omitempty" json:"parameters,omitempty"`
}

func (v *ValidatorInline) EffectiveWeight() float64 {
	if v.Weight <= 0 {
		return 1.0
	}
	return v.Weight
}

func (v *ValidatorInline) UnmarshalYAML(node *yaml.Node) error {
	// We need to unmarshal into a separate struct to apply KnownFields strict parsing, since ValidatorInline has flexible fields based on the Kind.
	type rawValidatorInline struct {
		Identifier string     `yaml:"name"`
		Kind       GraderKind `yaml:"type,omitempty"`
		Checks     []string   `yaml:"assertions,omitempty"`
		Rubric     string     `yaml:"rubric,omitempty"`
		Weight     float64    `yaml:"weight,omitempty"`
		Parameters yaml.Node  `yaml:"config,omitempty"`
	}

	var raw rawValidatorInline

	// Serialize the node back to bytes to leverage KnownFields strict parsing on the raw struct
	bytesData, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("failed to marshal validator config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(bytesData))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		return err
	}

	params, err := decodeGraderParameters(raw.Kind, &raw.Parameters)
	if err != nil {
		return fmt.Errorf("invalid grader config for %q (type %q): %w", raw.Identifier, raw.Kind, err)
	}

	v.Identifier = raw.Identifier
	v.Kind = raw.Kind
	v.Checks = raw.Checks
	v.Rubric = raw.Rubric
	v.Weight = raw.Weight
	v.Parameters = params

	// Validate grader-type-specific required fields
	if err := v.Validate(); err != nil {
		return err
	}

	return nil
}

// Validate checks that the validator config has required fields for its type.
func (v *ValidatorInline) Validate() error {
	switch v.Kind {
	case GraderKindInlineScript:
		params, ok := v.Parameters.(InlineScriptGraderParameters)
		if !ok {
			return fmt.Errorf("code grader %q: expected InlineScriptGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.Assertions) == 0 {
			return fmt.Errorf("code grader %q: must have at least one assertion in config.assertions", v.Identifier)
		}

	case GraderKindDiff:
		params, ok := v.Parameters.(DiffGraderParameters)
		if !ok {
			return fmt.Errorf("diff grader %q: expected DiffGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.ExpectedFiles) == 0 {
			return fmt.Errorf("diff grader %q: must have at least one file in config.expected_files", v.Identifier)
		}

	case GraderKindJSONSchema:
		params, ok := v.Parameters.(JSONSchemaGraderParameters)
		if !ok {
			return fmt.Errorf("json_schema grader %q: expected JSONSchemaGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if params.Schema == nil && params.SchemaFile == "" {
			return fmt.Errorf("json_schema grader %q: must specify either config.schema or config.schema_file", v.Identifier)
		}

	case GraderKindProgram:
		params, ok := v.Parameters.(ProgramGraderParameters)
		if !ok {
			return fmt.Errorf("program grader %q: expected ProgramGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if params.Command == "" {
			return fmt.Errorf("program grader %q: must specify config.command", v.Identifier)
		}

	case GraderKindTrigger:
		params, ok := v.Parameters.(TriggerHeuristicGraderParameters)
		if !ok {
			return fmt.Errorf("trigger grader %q: expected TriggerHeuristicGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if params.SkillPath == "" {
			return fmt.Errorf("trigger grader %q: must specify config.skill_path", v.Identifier)
		}

	case GraderKindActionSequence:
		params, ok := v.Parameters.(ActionSequenceGraderParameters)
		if !ok {
			return fmt.Errorf("action_sequence grader %q: expected ActionSequenceGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.ExpectedActions) == 0 {
			return fmt.Errorf("action_sequence grader %q: must have at least one action in config.expected_actions", v.Identifier)
		}

	case GraderKindSkillInvocation:
		params, ok := v.Parameters.(SkillInvocationGraderParameters)
		if !ok {
			return fmt.Errorf("skill_invocation grader %q: expected SkillInvocationGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.RequiredSkills) == 0 {
			return fmt.Errorf("skill_invocation grader %q: must have at least one skill in config.required_skills", v.Identifier)
		}

	case GraderKindToolConstraint:
		params, ok := v.Parameters.(ToolConstraintGraderParameters)
		if !ok {
			return fmt.Errorf("tool_constraint grader %q: expected ToolConstraintGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.ExpectTools) == 0 && len(params.RejectTools) == 0 {
			return fmt.Errorf("tool_constraint grader %q: must have at least one tool in config.expect_tools or config.reject_tools", v.Identifier)
		}

	case GraderKindFile:
		params, ok := v.Parameters.(FileGraderParameters)
		if !ok {
			return fmt.Errorf("file grader %q: expected FileGraderParameters, got %T", v.Identifier, v.Parameters)
		}
		if len(params.MustExist) == 0 && len(params.MustNotExist) == 0 && len(params.ContentPatterns) == 0 {
			return fmt.Errorf("file grader %q: must specify at least one of config.must_exist, config.must_not_exist, or config.content_patterns", v.Identifier)
		}

		// GraderKindText, GraderKindBehavior, GraderKindPrompt allow empty configs
	}

	return nil
}

// LoadTestCase loads a test case from YAML
func LoadTestCase(path string) (*TestCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tc TestCase
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true) // Strict parsing to catch unknown fields
	if err := decoder.Decode(&tc); err != nil {
		return nil, fmt.Errorf("parsing test case YAML: %w", err)
	}

	// Note: Active field defaults to nil when not specified in YAML.
	// The runner treats nil as true (enabled by default).
	// Only explicitly set "enabled: false" will disable a test.

	// Resolve prompt_file into the prompt message
	if err := tc.Stimulus.resolvePromptFile(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("test case %s: %w", path, err)
	}

	return &tc, nil
}

// resolvePromptFile loads prompt content from a file if prompt_file is set.
// The path is resolved relative to baseDir. Absolute and traversal paths are
// rejected, consistent with resource path validation in the runner.
func (s *TaskStimulus) resolvePromptFile(baseDir string) error {
	if s.MessageFile == "" {
		return nil
	}
	if s.Message != "" {
		return fmt.Errorf("cannot specify both prompt and prompt_file")
	}

	target := s.MessageFile
	if filepath.IsAbs(target) {
		return fmt.Errorf("prompt_file must be a relative path, got %q", target)
	}
	clean := filepath.Clean(target)
	if strings.Contains(clean, "..") {
		return fmt.Errorf("prompt_file must not contain path traversal, got %q", target)
	}

	resolved := filepath.Join(baseDir, clean)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("reading prompt_file %q: %w", s.MessageFile, err)
	}

	s.Message = string(data)
	s.MessageFile = "" // clear to avoid leaking file paths in serialized output
	return nil
}

// Deprecated: Use TaskStimulus instead.
type TestStimulus = TaskStimulus

// Deprecated: Use TaskExpectation instead.
type TestExpectation = TaskExpectation
