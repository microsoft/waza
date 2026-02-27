// Package projectconfig provides the ProjectConfig struct and loader for
// .waza.yaml project-level configuration files.
package projectconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Default values for project configuration. These are the single source of
// truth — New() references them and no other code should duplicate them.
const (
	DefaultSkillsDir  = "skills/"
	DefaultEvalsDir   = "evals/"
	DefaultResultsDir = "results/"

	DefaultEngine  = "copilot-sdk"
	DefaultModel   = "claude-sonnet-4.6"
	DefaultTimeout = 300
	DefaultWorkers = 4

	DefaultCacheDir = ".waza-cache"

	DefaultServerPort       = 3000
	DefaultServerResultsDir = "results/"

	DefaultDevModel         = "claude-sonnet-4-20250514"
	DefaultDevTarget        = "medium-high"
	DefaultDevMaxIterations = 5

	DefaultTokenWarningThreshold = 500
	DefaultTokenFallbackLimit    = 1000

	DefaultGraderProgramTimeout = 30

	DefaultStorageContainerName = "waza-results"
)

// PathsConfig holds directory paths for skills, evals, and results.
type PathsConfig struct {
	Skills  string `yaml:"skills,omitempty"`
	Evals   string `yaml:"evals,omitempty"`
	Results string `yaml:"results,omitempty"`
}

// DefaultsConfig holds default execution parameters.
type DefaultsConfig struct {
	Engine     string `yaml:"engine,omitempty"`
	Model      string `yaml:"model,omitempty"`
	JudgeModel string `yaml:"judgeModel,omitempty"`
	Timeout    int    `yaml:"timeout,omitempty"`
	Parallel   *bool  `yaml:"parallel,omitempty"`
	Workers    int    `yaml:"workers,omitempty"`
	Verbose    *bool  `yaml:"verbose,omitempty"`
	SessionLog *bool  `yaml:"sessionLog,omitempty"`
}

// CacheConfig holds cache settings.
type CacheConfig struct {
	Enabled *bool  `yaml:"enabled,omitempty"`
	Dir     string `yaml:"dir,omitempty"`
}

// ServerConfig holds dashboard server settings.
type ServerConfig struct {
	Port       int    `yaml:"port,omitempty"`
	ResultsDir string `yaml:"resultsDir,omitempty"`
}

// DevConfig holds waza dev command settings.
type DevConfig struct {
	Model         string `yaml:"model,omitempty"`
	Target        string `yaml:"target,omitempty"`
	MaxIterations int    `yaml:"maxIterations,omitempty"`
}

// TokenLimitsConfig holds per-model token limit maps.
type TokenLimitsConfig struct {
	Defaults  map[string]int `yaml:"defaults,omitempty"`
	Overrides map[string]int `yaml:"overrides,omitempty"`
}

// TokensConfig holds token budget settings.
type TokensConfig struct {
	WarningThreshold int                `yaml:"warningThreshold,omitempty"`
	FallbackLimit    int                `yaml:"fallbackLimit,omitempty"`
	Limits           *TokenLimitsConfig `yaml:"limits,omitempty"`
}

// GradersConfig holds grader execution settings.
type GradersConfig struct {
	ProgramTimeout int `yaml:"programTimeout,omitempty"`
}

// StorageConfig holds remote result storage settings.
type StorageConfig struct {
	Provider      string `yaml:"provider,omitempty"`      // "azure-blob" or empty for local
	AccountName   string `yaml:"accountName,omitempty"`   // Azure storage account name
	ContainerName string `yaml:"containerName,omitempty"` // blob container (default: "waza-results")
	Enabled       bool   `yaml:"enabled,omitempty"`       // true when remote storage is configured
}

// ProjectConfig is the top-level configuration loaded from .waza.yaml.
type ProjectConfig struct {
	Paths    PathsConfig    `yaml:"paths,omitempty"`
	Defaults DefaultsConfig `yaml:"defaults,omitempty"`
	Cache    CacheConfig    `yaml:"cache,omitempty"`
	Server   ServerConfig   `yaml:"server,omitempty"`
	Dev      DevConfig      `yaml:"dev,omitempty"`
	Tokens   TokensConfig   `yaml:"tokens,omitempty"`
	Graders  GradersConfig  `yaml:"graders,omitempty"`
	Storage  StorageConfig  `yaml:"storage,omitempty"`
}

// New returns a ProjectConfig with all hard-coded defaults populated.
func New() *ProjectConfig {
	return &ProjectConfig{
		Paths: PathsConfig{
			Skills:  DefaultSkillsDir,
			Evals:   DefaultEvalsDir,
			Results: DefaultResultsDir,
		},
		Defaults: DefaultsConfig{
			Engine:     DefaultEngine,
			Model:      DefaultModel,
			JudgeModel: "",
			Timeout:    DefaultTimeout,
			Parallel:   boolPtr(false),
			Workers:    DefaultWorkers,
			Verbose:    boolPtr(false),
			SessionLog: boolPtr(false),
		},
		Cache: CacheConfig{
			Enabled: boolPtr(false),
			Dir:     DefaultCacheDir,
		},
		Server: ServerConfig{
			Port:       DefaultServerPort,
			ResultsDir: DefaultResultsDir,
		},
		Dev: DevConfig{
			Model:         DefaultDevModel,
			Target:        DefaultDevTarget,
			MaxIterations: DefaultDevMaxIterations,
		},
		Tokens: TokensConfig{
			WarningThreshold: DefaultTokenWarningThreshold,
			FallbackLimit:    DefaultTokenFallbackLimit,
		},
		Graders: GradersConfig{
			ProgramTimeout: DefaultGraderProgramTimeout,
		},
		Storage: StorageConfig{
			ContainerName: DefaultStorageContainerName,
		},
	}
}

// Load finds .waza.yaml by walking up from startDir (max 10 levels),
// unmarshals it, and fills in missing fields with defaults.
// If no config file is found, returns defaults with a nil error.
// Real I/O errors (e.g. permission denied) are returned to the caller.
func Load(startDir string) (*ProjectConfig, error) {
	cfg := New()

	data, err := findConfigFile(startDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil // no file found → return defaults
		}
		return nil, fmt.Errorf("loading .waza.yaml: %w", err)
	}

	var fileCfg ProjectConfig
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return nil, fmt.Errorf("parsing .waza.yaml: %w", err)
	}

	// Merge file values onto defaults.
	mergeConfig(cfg, &fileCfg)
	return cfg, nil
}

// findConfigFile walks up from dir looking for .waza.yaml (max 10 levels).
// Returns os.ErrNotExist if no config file is found. Propagates real I/O
// errors (e.g. permission denied) instead of silently swallowing them.
func findConfigFile(dir string) ([]byte, error) {
	// Convert to absolute path so filepath.Dir(".") walks correctly.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving path %q: %w", dir, err)
	}
	dir = absDir

	for i := 0; i < 10; i++ {
		p := filepath.Join(dir, ".waza.yaml")
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("reading %q: %w", p, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached filesystem root
		}
		dir = parent
	}
	return nil, os.ErrNotExist
}

// mergeConfig overlays non-zero values from src onto dst.
func mergeConfig(dst, src *ProjectConfig) {
	// Paths
	if src.Paths.Skills != "" {
		dst.Paths.Skills = src.Paths.Skills
	}
	if src.Paths.Evals != "" {
		dst.Paths.Evals = src.Paths.Evals
	}
	if src.Paths.Results != "" {
		dst.Paths.Results = src.Paths.Results
	}

	// Defaults
	if src.Defaults.Engine != "" {
		dst.Defaults.Engine = src.Defaults.Engine
	}
	if src.Defaults.Model != "" {
		dst.Defaults.Model = src.Defaults.Model
	}
	if src.Defaults.JudgeModel != "" {
		dst.Defaults.JudgeModel = src.Defaults.JudgeModel
	}
	if src.Defaults.Timeout != 0 {
		dst.Defaults.Timeout = src.Defaults.Timeout
	}
	if src.Defaults.Parallel != nil {
		dst.Defaults.Parallel = src.Defaults.Parallel
	}
	if src.Defaults.Workers != 0 {
		dst.Defaults.Workers = src.Defaults.Workers
	}
	if src.Defaults.Verbose != nil {
		dst.Defaults.Verbose = src.Defaults.Verbose
	}
	if src.Defaults.SessionLog != nil {
		dst.Defaults.SessionLog = src.Defaults.SessionLog
	}

	// Cache
	if src.Cache.Enabled != nil {
		dst.Cache.Enabled = src.Cache.Enabled
	}
	if src.Cache.Dir != "" {
		dst.Cache.Dir = src.Cache.Dir
	}

	// Server
	if src.Server.Port != 0 {
		dst.Server.Port = src.Server.Port
	}
	if src.Server.ResultsDir != "" {
		dst.Server.ResultsDir = src.Server.ResultsDir
	}

	// Dev
	if src.Dev.Model != "" {
		dst.Dev.Model = src.Dev.Model
	}
	if src.Dev.Target != "" {
		dst.Dev.Target = src.Dev.Target
	}
	if src.Dev.MaxIterations != 0 {
		dst.Dev.MaxIterations = src.Dev.MaxIterations
	}

	// Tokens
	if src.Tokens.WarningThreshold != 0 {
		dst.Tokens.WarningThreshold = src.Tokens.WarningThreshold
	}
	if src.Tokens.FallbackLimit != 0 {
		dst.Tokens.FallbackLimit = src.Tokens.FallbackLimit
	}
	if src.Tokens.Limits != nil {
		dst.Tokens.Limits = src.Tokens.Limits
	}

	// Graders
	if src.Graders.ProgramTimeout != 0 {
		dst.Graders.ProgramTimeout = src.Graders.ProgramTimeout
	}

	// Storage
	if src.Storage.Provider != "" {
		dst.Storage.Provider = src.Storage.Provider
	}
	if src.Storage.AccountName != "" {
		dst.Storage.AccountName = src.Storage.AccountName
	}
	if src.Storage.ContainerName != "" {
		dst.Storage.ContainerName = src.Storage.ContainerName
	}
	if src.Storage.Enabled {
		dst.Storage.Enabled = true
	}
}

func boolPtr(b bool) *bool {
	return &b
}
