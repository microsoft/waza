package models

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/hooks"
	"gopkg.in/yaml.v3"
)

// BenchmarkSpec represents a complete evaluation specification
type BenchmarkSpec struct {
	SpecIdentity `yaml:",inline"`
	SkillName    string            `yaml:"skill"`
	Version      string            `yaml:"version"`
	Config       Config            `yaml:"config"`
	Hooks        hooks.HooksConfig `yaml:"hooks,omitempty"`
	Inputs       map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	TasksFrom    string            `yaml:"tasks_from,omitempty" json:"tasks_from,omitempty"`
	Range        [2]int            `yaml:"range,omitempty" json:"range,omitempty"`
	Graders      []GraderConfig    `yaml:"graders"`
	Metrics      []MeasurementDef  `yaml:"metrics"`
	Tasks        []string          `yaml:"tasks"`
	Baseline     bool              `yaml:"baseline,omitempty" json:"baseline,omitempty"`
}

type SpecIdentity struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Config controls execution behavior
type Config struct {
	TrialsPerTask  int            `yaml:"trials_per_task" json:"runs_per_test"`
	TimeoutSec     int            `yaml:"timeout_seconds" json:"timeout_sec"`
	Concurrent     bool           `yaml:"parallel" json:"concurrent"`
	Workers        int            `yaml:"max_workers,omitempty" json:"workers,omitempty"`
	StopOnError    bool           `yaml:"fail_fast,omitempty" json:"stop_on_error,omitempty"`
	EngineType     string         `yaml:"executor" json:"engine_type"`
	ModelID        string         `yaml:"model" json:"model_id"`
	SkillPaths     []string       `yaml:"skill_directories,omitempty" json:"skill_paths,omitempty"`
	RequiredSkills []string       `yaml:"required_skills,omitempty" json:"required_skills,omitempty"`
	ServerConfigs  map[string]any `yaml:"mcp_servers,omitempty" json:"server_configs,omitempty"`
	MaxAttempts    int            `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	GroupBy        string         `yaml:"group_by,omitempty" json:"group_by,omitempty"`
	JudgeModel     string         `yaml:"judge_model,omitempty" json:"judge_model,omitempty"`
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
	if err := node.Decode(&raw); err != nil {
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

	return nil
}

// EffectiveWeight returns the grader weight, defaulting to 1.0 if unset.
func (g *GraderConfig) EffectiveWeight() float64 {
	if g.Weight <= 0 {
		return 1.0
	}
	return g.Weight
}

// MeasurementDef defines a metric
type MeasurementDef struct {
	Identifier string  `yaml:"name" json:"identifier"`
	Weight     float64 `yaml:"weight" json:"weight"`
	Threshold  float64 `yaml:"threshold" json:"threshold"`
	Enabled    bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Desc       string  `yaml:"description,omitempty" json:"desc,omitempty"`
}

// LoadBenchmarkSpec loads a spec from a YAML file
func LoadBenchmarkSpec(path string) (*BenchmarkSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var spec BenchmarkSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}

	// Validate spec
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	return &spec, nil
}

// Validate checks that the spec is valid
func (s *BenchmarkSpec) Validate() error {
	if s.Config.TrialsPerTask < 1 {
		return fmt.Errorf("trials_per_task must be at least 1, got %d", s.Config.TrialsPerTask)
	}
	if s.Config.TimeoutSec < 1 {
		return fmt.Errorf("timeout_seconds must be at least 1, got %d", s.Config.TimeoutSec)
	}
	return nil
}

// ResolveTestFiles expands glob patterns to actual test files
func (s *BenchmarkSpec) ResolveTestFiles(basePath string) ([]string, error) {
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
