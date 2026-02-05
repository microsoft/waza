package models

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// BenchmarkSpec represents a complete evaluation specification
type BenchmarkSpec struct {
	Identity       SpecIdentity      `yaml:"name,inline" json:"identity"`
	SkillName      string            `yaml:"skill" json:"skill_name"`
	Version        string            `yaml:"version" json:"version"`
	RuntimeOptions RuntimeOptions    `yaml:"config" json:"runtime_options"`
	Validators     []ValidatorConfig `yaml:"graders" json:"validators"`
	Measurements   []MeasurementDef  `yaml:"metrics" json:"measurements"`
	TestPatterns   []string          `yaml:"tasks" json:"test_patterns"`
}

type SpecIdentity struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// RuntimeOptions controls execution behavior
type RuntimeOptions struct {
	RunsPerTest     int               `yaml:"trials_per_task" json:"runs_per_test"`
	TimeoutSec      int               `yaml:"timeout_seconds" json:"timeout_sec"`
	Concurrent      bool              `yaml:"parallel" json:"concurrent"`
	Workers         int               `yaml:"max_workers,omitempty" json:"workers,omitempty"`
	StopOnError     bool              `yaml:"fail_fast,omitempty" json:"stop_on_error,omitempty"`
	EngineType      string            `yaml:"executor" json:"engine_type"`
	ModelID         string            `yaml:"model" json:"model_id"`
	SkillPaths      []string          `yaml:"skill_directories,omitempty" json:"skill_paths,omitempty"`
	ServerConfigs   map[string]any    `yaml:"mcp_servers,omitempty" json:"server_configs,omitempty"`
}

// ValidatorConfig defines a validator/grader
type ValidatorConfig struct {
	Kind       string         `yaml:"type" json:"kind"`
	Identifier string         `yaml:"name" json:"identifier"`
	ScriptPath string         `yaml:"script,omitempty" json:"script_path,omitempty"`
	Rubric     string         `yaml:"rubric,omitempty" json:"rubric,omitempty"`
	ModelID    string         `yaml:"model,omitempty" json:"model_id,omitempty"`
	Parameters map[string]any `yaml:"config,omitempty" json:"parameters,omitempty"`
}

// MeasurementDef defines a metric
type MeasurementDef struct {
	Identifier string  `yaml:"name" json:"identifier"`
	Weight     float64 `yaml:"weight" json:"weight"`
	Cutoff     float64 `yaml:"threshold" json:"cutoff"`
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

	return &spec, nil
}

// ResolveTestFiles expands glob patterns to actual test files
func (s *BenchmarkSpec) ResolveTestFiles(basePath string) ([]string, error) {
	var files []string
	for _, pattern := range s.TestPatterns {
		fullPattern := filepath.Join(basePath, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	return files, nil
}
