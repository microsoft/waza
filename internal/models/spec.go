package models

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/hooks"
	"gopkg.in/yaml.v3"
)

// EvalSpec represents a complete evaluation specification.
//
// Deprecated alias: BenchmarkSpec is provided for backward compatibility.
type EvalSpec struct {
	SchemaVersion string `yaml:"schemaVersion,omitempty" json:"schemaVersion,omitempty"`
	SpecIdentity  `yaml:",inline"`
	SkillName     string             `yaml:"skill"`
	Version       string             `yaml:"version"`
	Config        Config             `yaml:"config"`
	Hooks         hooks.HooksConfig  `yaml:"hooks,omitempty"`
	MCPMocks      []MCPMockConfig    `yaml:"mcp_mocks,omitempty" json:"mcp_mocks,omitempty"`
	Adversarial   *AdversarialConfig `yaml:"adversarial,omitempty" json:"adversarial,omitempty"`
	Inputs        map[string]string  `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	TasksFrom     string             `yaml:"tasks_from,omitempty" json:"tasks_from,omitempty"`
	Range         [2]int             `yaml:"range,omitempty" json:"range,omitempty"`
	Graders       []GraderConfig     `yaml:"graders"`
	Metrics       []MeasurementDef   `yaml:"metrics"`
	Tasks         []string           `yaml:"tasks"`
	Baseline      bool               `yaml:"baseline,omitempty" json:"baseline,omitempty"`
}

type SpecIdentity struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type strictEvalSpec struct {
	SchemaVersion string `yaml:"schemaVersion,omitempty"`
	SpecIdentity  `yaml:",inline"`
	SkillName     string             `yaml:"skill"`
	Version       string             `yaml:"version"`
	Config        Config             `yaml:"config"`
	Hooks         hooks.HooksConfig  `yaml:"hooks,omitempty"`
	MCPMocks      []MCPMockConfig    `yaml:"mcp_mocks,omitempty"`
	Adversarial   *AdversarialConfig `yaml:"adversarial,omitempty"`
	Inputs        map[string]string  `yaml:"inputs,omitempty"`
	TasksFrom     string             `yaml:"tasks_from,omitempty"`
	Range         [2]int             `yaml:"range,omitempty"`
	Graders       []strictGrader     `yaml:"graders"`
	Metrics       []MeasurementDef   `yaml:"metrics"`
	Tasks         []string           `yaml:"tasks"`
	Baseline      bool               `yaml:"baseline,omitempty"`
}

type strictGrader struct {
	Kind       GraderKind `yaml:"type"`
	Identifier string     `yaml:"name"`
	ScriptPath string     `yaml:"script,omitempty"`
	Rubric     string     `yaml:"rubric,omitempty"`
	ModelID    string     `yaml:"model,omitempty"`
	Weight     float64    `yaml:"weight,omitempty"`
	Parameters yaml.Node  `yaml:"config,omitempty"`
}

// Config controls execution behavior
type Config struct {
	TrialsPerTask int `yaml:"trials_per_task" json:"runs_per_test"`
	TimeoutSec    int `yaml:"timeout_seconds" json:"timeout_sec"`
	// FirstEventTimeoutSec bounds how long to wait for the first session event
	// before treating a run as a session-start hang (the engine launched but
	// never started the agent's first turn). It is much shorter than
	// TimeoutSec, which must cover the slowest legitimate full turn. 0 (the
	// default) disables the check. Per-task overrides live on TestCase.
	FirstEventTimeoutSec int            `yaml:"first_event_timeout_seconds,omitempty" json:"first_event_timeout_sec,omitempty"`
	Concurrent           bool           `yaml:"parallel" json:"concurrent"`
	Workers              int            `yaml:"workers,omitempty" json:"workers,omitempty"`
	StopOnError          bool           `yaml:"fail_fast,omitempty" json:"stop_on_error,omitempty"`
	EngineType           string         `yaml:"executor" json:"engine_type"`
	ModelID              string         `yaml:"model" json:"model_id"`
	SkillPaths           []string       `yaml:"skill_directories,omitempty" json:"skill_paths,omitempty"`
	InstructionFiles     []string       `yaml:"instruction_files,omitempty" json:"instruction_files,omitempty"`
	InjectSkillBody      *bool          `yaml:"inject_skill_body,omitempty" json:"inject_skill_body,omitempty"`
	DisabledSkills       []string       `yaml:"disabled_skills,omitempty" json:"disabled_skills,omitempty"`
	RequiredSkills       []string       `yaml:"required_skills,omitempty" json:"required_skills,omitempty"`
	ServerConfigs        map[string]any `yaml:"mcp_servers,omitempty" json:"server_configs,omitempty"`
	MaxAttempts          int            `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	GroupBy              string         `yaml:"group_by,omitempty" json:"group_by,omitempty"`
	JudgeModel           string         `yaml:"judge_model,omitempty" json:"judge_model,omitempty"`
}

// MCPMockConfig defines a deterministic MCP server mock launched for an eval.
type MCPMockConfig struct {
	Name     string                 `yaml:"name" json:"name"`
	Fixtures string                 `yaml:"fixtures,omitempty" json:"fixtures,omitempty"`
	Tools    map[string]MCPMockTool `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// AdversarialOnUnsafeOutcome controls how unsafe outcomes from adversarial
// packs are reported to the caller. It is consumed by `waza adversarial` to
// decide whether an unsafe outcome should fail CI or only warn.
type AdversarialOnUnsafeOutcome string

const (
	// AdversarialOnUnsafeOutcomeFail (default) makes any unsafe outcome a hard
	// failure: `waza adversarial` exits with code 2 (matching `waza gate`'s
	// golden-failure exit) so a single CI step can gate both signals.
	AdversarialOnUnsafeOutcomeFail AdversarialOnUnsafeOutcome = "fail"
	// AdversarialOnUnsafeOutcomeWarn keeps the exit code at zero and only
	// records the unsafe outcome in the results.json + stderr summary.
	AdversarialOnUnsafeOutcomeWarn AdversarialOnUnsafeOutcome = "warn"
)

// AdversarialConfig declares offline adversarial / fault-injection packs to
// run against the skill under test. Packs ship as deterministic, embedded
// YAML + fixture bundles so they execute identically in CI without any live
// payload generation. See `waza adversarial` for the runner.
type AdversarialConfig struct {
	// Packs is the list of built-in pack identifiers to run (e.g.
	// "prompt-injection", "scope-bypass"). Unknown identifiers are rejected
	// by `waza adversarial`; the current schema does not support
	// out-of-tree custom packs. Required; must contain at least one entry.
	Packs []string `yaml:"packs" json:"packs"`
	// OnUnsafeOutcome controls whether unsafe outcomes fail the run
	// ("fail", default) or only warn ("warn"). When empty, "fail" is used.
	OnUnsafeOutcome AdversarialOnUnsafeOutcome `yaml:"on_unsafe_outcome,omitempty" json:"on_unsafe_outcome,omitempty"`
}

// EffectiveOnUnsafeOutcome returns the configured policy, defaulting to
// AdversarialOnUnsafeOutcomeFail when unset.
func (a *AdversarialConfig) EffectiveOnUnsafeOutcome() AdversarialOnUnsafeOutcome {
	if a == nil || a.OnUnsafeOutcome == "" {
		return AdversarialOnUnsafeOutcomeFail
	}
	return a.OnUnsafeOutcome
}

// Validate checks the adversarial config has at least one pack and that the
// on_unsafe_outcome policy is one of the supported values. Pack names are
// normalized in-place (whitespace trimmed) so downstream lookups see the
// canonical form regardless of YAML formatting.
func (a *AdversarialConfig) Validate() error {
	if a == nil {
		return nil
	}
	if len(a.Packs) == 0 {
		return fmt.Errorf("adversarial.packs must list at least one pack")
	}
	seen := make(map[string]bool, len(a.Packs))
	for i, p := range a.Packs {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("adversarial.packs[%d] is empty", i)
		}
		if seen[p] {
			return fmt.Errorf("adversarial.packs[%d] %q is duplicated", i, p)
		}
		seen[p] = true
		a.Packs[i] = p
	}
	switch a.OnUnsafeOutcome {
	case "", AdversarialOnUnsafeOutcomeFail, AdversarialOnUnsafeOutcomeWarn:
	default:
		return fmt.Errorf("adversarial.on_unsafe_outcome must be %q or %q, got %q",
			AdversarialOnUnsafeOutcomeFail, AdversarialOnUnsafeOutcomeWarn, a.OnUnsafeOutcome)
	}
	return nil
}

// MCPMockTool defines a mocked MCP tool and its response fixtures.
type MCPMockTool struct {
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	InputSchema map[string]any    `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
	Responses   []MCPMockResponse `yaml:"responses,omitempty" json:"responses,omitempty"`
}

// MCPMockResponse defines one deterministic response for a mocked tool call.
type MCPMockResponse struct {
	Match       map[string]any    `yaml:"match,omitempty" json:"match,omitempty"`
	MatchSchema map[string]any    `yaml:"match_schema,omitempty" json:"match_schema,omitempty"`
	MatchRegex  map[string]string `yaml:"match_regex,omitempty" json:"match_regex,omitempty"`
	Return      any               `yaml:"return,omitempty" json:"return,omitempty"`
	Error       string            `yaml:"error,omitempty" json:"error,omitempty"`
}

// ShouldInjectSkillBody returns true unless the eval explicitly opts out of
// injecting the target skill body into the system prompt.
func (c *Config) ShouldInjectSkillBody() bool {
	return c.InjectSkillBody == nil || *c.InjectSkillBody
}

// GraderConfig defines a validator/grader
type GraderConfig struct {
	Kind       GraderKind       `yaml:"type" json:"kind"`
	Identifier string           `yaml:"name" json:"identifier"`
	ScriptPath string           `yaml:"script,omitempty" json:"script_path,omitempty"`
	Rubric     string           `yaml:"rubric,omitempty" json:"rubric,omitempty"`
	ModelID    string           `yaml:"model,omitempty" json:"model_id,omitempty"`
	Weight     float64          `yaml:"weight,omitempty" json:"weight,omitempty"`
	Parameters GraderParameters `yaml:"config,omitempty" json:"parameters,omitempty"`
}

func (g *GraderConfig) UnmarshalYAML(node *yaml.Node) error {
	type rawGraderConfig struct {
		Kind       GraderKind `yaml:"type"`
		Identifier string     `yaml:"name"`
		ScriptPath string     `yaml:"script,omitempty"`
		Rubric     string     `yaml:"rubric,omitempty"`
		ModelID    string     `yaml:"model,omitempty"`
		Weight     float64    `yaml:"weight,omitempty"`
		Parameters yaml.Node  `yaml:"config,omitempty"`
	}

	var raw rawGraderConfig
	bytesData, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("failed to marshal grader config: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(bytesData))
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := warnUnknownYAMLFields(bytesData, "grader", fmt.Sprintf("grader %q", raw.Identifier), &rawGraderConfig{}); err != nil {
		return err
	}

	params, err := decodeGraderParameters(raw.Kind, &raw.Parameters)
	if err != nil {
		return fmt.Errorf("invalid grader config for %q (type %q): %w", raw.Identifier, raw.Kind, err)
	}

	g.Kind = raw.Kind
	g.Identifier = raw.Identifier
	g.ScriptPath = raw.ScriptPath
	g.Rubric = raw.Rubric
	g.ModelID = raw.ModelID
	g.Weight = raw.Weight
	g.Parameters = params

	// Validate grader-type-specific required fields
	if err := g.Validate(); err != nil {
		return err
	}

	return nil
}

// EffectiveWeight returns the grader weight, defaulting to 1.0 if unset.
func (g *GraderConfig) EffectiveWeight() float64 {
	if g.Weight <= 0 {
		return 1.0
	}
	return g.Weight
}

// Validate checks that the grader config has required fields for its type.
func (g *GraderConfig) Validate() error {
	switch g.Kind {
	case GraderKindInlineScript:
		params, ok := g.Parameters.(InlineScriptGraderParameters)
		if !ok {
			return fmt.Errorf("code grader %q: expected InlineScriptGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.Assertions) == 0 {
			return fmt.Errorf("code grader %q: must have at least one assertion in config.assertions", g.Identifier)
		}

	case GraderKindDiff:
		params, ok := g.Parameters.(DiffGraderParameters)
		if !ok {
			return fmt.Errorf("diff grader %q: expected DiffGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.ExpectedFiles) == 0 {
			return fmt.Errorf("diff grader %q: must have at least one file in config.expected_files", g.Identifier)
		}

	case GraderKindJSONSchema:
		params, ok := g.Parameters.(JSONSchemaGraderParameters)
		if !ok {
			return fmt.Errorf("json_schema grader %q: expected JSONSchemaGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if params.Schema == nil && params.SchemaFile == "" {
			return fmt.Errorf("json_schema grader %q: must specify either config.schema or config.schema_file", g.Identifier)
		}

	case GraderKindProgram:
		params, ok := g.Parameters.(ProgramGraderParameters)
		if !ok {
			return fmt.Errorf("program grader %q: expected ProgramGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if params.Command == "" {
			return fmt.Errorf("program grader %q: must specify config.command", g.Identifier)
		}

	case GraderKindTrigger:
		params, ok := g.Parameters.(TriggerHeuristicGraderParameters)
		if !ok {
			return fmt.Errorf("trigger grader %q: expected TriggerHeuristicGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if params.SkillPath == "" {
			return fmt.Errorf("trigger grader %q: must specify config.skill_path", g.Identifier)
		}

	case GraderKindActionSequence:
		params, ok := g.Parameters.(ActionSequenceGraderParameters)
		if !ok {
			return fmt.Errorf("action_sequence grader %q: expected ActionSequenceGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.ExpectedActions) == 0 {
			return fmt.Errorf("action_sequence grader %q: must have at least one action in config.expected_actions", g.Identifier)
		}

	case GraderKindSkillInvocation:
		params, ok := g.Parameters.(SkillInvocationGraderParameters)
		if !ok {
			return fmt.Errorf("skill_invocation grader %q: expected SkillInvocationGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.RequiredSkills) == 0 && len(params.ForbiddenSkills) == 0 {
			return fmt.Errorf("skill_invocation grader %q: must have at least one skill in config.required_skills or config.forbidden_skills", g.Identifier)
		}

	case GraderKindToolConstraint:
		params, ok := g.Parameters.(ToolConstraintGraderParameters)
		if !ok {
			return fmt.Errorf("tool_constraint grader %q: expected ToolConstraintGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.ExpectTools) == 0 && len(params.RejectTools) == 0 {
			return fmt.Errorf("tool_constraint grader %q: must have at least one tool in config.expect_tools or config.reject_tools", g.Identifier)
		}

	case GraderKindFile:
		params, ok := g.Parameters.(FileGraderParameters)
		if !ok {
			return fmt.Errorf("file grader %q: expected FileGraderParameters, got %T", g.Identifier, g.Parameters)
		}
		if len(params.MustExist) == 0 && len(params.MustNotExist) == 0 && len(params.ContentPatterns) == 0 {
			return fmt.Errorf("file grader %q: must specify at least one of config.must_exist, config.must_not_exist, or config.content_patterns", g.Identifier)
		}

		// GraderKindText, GraderKindBehavior, GraderKindPrompt allow empty configs
	}

	return nil
}

// AllSkillsDisabled returns true when skills should be completely disabled.
func (c *Config) AllSkillsDisabled() bool {
	for _, s := range c.DisabledSkills {
		if s == "*" {
			return true
		}
	}
	for _, s := range c.SkillPaths {
		if s == "none" {
			return true
		}
	}
	return false
}

// FilteredSkillPaths returns SkillPaths with any disabled skill directories removed.
func (c *Config) FilteredSkillPaths() []string {
	if c.AllSkillsDisabled() {
		return nil
	}
	if len(c.DisabledSkills) == 0 {
		return c.SkillPaths
	}
	disabled := make(map[string]bool, len(c.DisabledSkills))
	for _, s := range c.DisabledSkills {
		disabled[s] = true
	}
	var filtered []string
	for _, p := range c.SkillPaths {
		if !disabled[p] && !disabled[filepath.Base(p)] {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// MeasurementDef defines a metric
type MeasurementDef struct {
	Identifier string  `yaml:"name" json:"identifier"`
	Weight     float64 `yaml:"weight" json:"weight"`
	Threshold  float64 `yaml:"threshold" json:"threshold"`
	Enabled    bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Desc       string  `yaml:"description,omitempty" json:"desc,omitempty"`
}

// LoadEvalSpec loads a spec from a YAML file with strict validation.
//
// Normally the schema validation will catch errors in the eval.yaml, but this also does
// strict YAML parsing to catch errors like unknown fields or type errors that the schema
// validation might miss.
func LoadEvalSpec(path string) (*EvalSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var header struct {
		SchemaVersion string `yaml:"schemaVersion"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parsing eval spec YAML (%s): %w", path, err)
	}
	version, err := ValidateSchemaVersion("eval.yaml", path, header.SchemaVersion)
	if err != nil {
		return nil, err
	}

	var spec EvalSpec

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("parsing eval spec YAML (%s): %w", path, err)
	}
	spec.SchemaVersion = version

	if err := warnUnknownYAMLFields(data, "eval.yaml", path, &strictEvalSpec{}); err != nil {
		return nil, fmt.Errorf("parsing eval spec YAML (%s): %w", path, err)
	}

	// Validate spec
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	return &spec, nil
}

// Validate checks that the spec is valid
func (s *EvalSpec) Validate() error {
	if len(s.MCPMocks) > 0 {
		_, minor, err := parseSchemaVersion(s.SchemaVersion)
		if err != nil {
			return err
		}
		if minor < 1 {
			return fmt.Errorf("mcp_mocks requires schemaVersion 1.1 or newer")
		}
		seen := make(map[string]bool, len(s.MCPMocks))
		for i, mock := range s.MCPMocks {
			name := strings.TrimSpace(mock.Name)
			if name == "" {
				return fmt.Errorf("mcp_mocks[%d].name is required", i)
			}
			if seen[name] {
				return fmt.Errorf("mcp_mocks[%d].name %q is duplicated", i, name)
			}
			seen[name] = true
		}
	}
	if s.Adversarial != nil {
		_, minor, err := parseSchemaVersion(s.SchemaVersion)
		if err != nil {
			return err
		}
		if minor < 2 {
			return fmt.Errorf("adversarial requires schemaVersion 1.2 or newer")
		}
		if err := s.Adversarial.Validate(); err != nil {
			return err
		}
	}
	if s.Config.TrialsPerTask < 1 {
		return fmt.Errorf("trials_per_task must be at least 1, got %d", s.Config.TrialsPerTask)
	}
	if s.Config.TimeoutSec < 1 {
		return fmt.Errorf("timeout_seconds must be at least 1, got %d", s.Config.TimeoutSec)
	}
	if s.Config.FirstEventTimeoutSec < 0 {
		return fmt.Errorf("first_event_timeout_seconds must not be negative, got %d", s.Config.FirstEventTimeoutSec)
	}
	return nil
}

// ResolveTestFiles expands glob patterns to actual test files
func (s *EvalSpec) ResolveTestFiles(basePath string) ([]string, error) {
	var files []string
	for _, pattern := range s.Tasks {
		fullPattern := filepath.Join(basePath, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	return files, nil
}

// Deprecated: Use EvalSpec instead.
type BenchmarkSpec = EvalSpec

// Deprecated: Use LoadEvalSpec instead.
func LoadBenchmarkSpec(path string) (*EvalSpec, error) {
	return LoadEvalSpec(path)
}
