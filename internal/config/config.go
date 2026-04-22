package config

import (
	"github.com/microsoft/waza/internal/models"
)

// EvalConfig is the main configuration with functional options.
//
// Deprecated alias: BenchmarkConfig is provided for backward compatibility.
type EvalConfig struct {
	spec          *models.EvalSpec
	specDir       string // Directory containing the spec file (for resolving test patterns)
	fixtureDir    string // Directory containing fixtures/context files
	verbose       bool
	outputPath    string
	logPath       string
	transcriptDir string // Directory for per-task transcript JSON files
}

// Option is a functional option for EvalConfig
type Option func(*EvalConfig)

// NewEvalConfig creates a new configuration with options
func NewEvalConfig(spec *models.EvalSpec, opts ...Option) *EvalConfig {
	cfg := &EvalConfig{
		spec:    spec,
		verbose: false,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// WithSpecDir sets the spec directory (for resolving test patterns)
func WithSpecDir(path string) Option {
	return func(c *EvalConfig) {
		c.specDir = path
	}
}

// WithFixtureDir sets the fixture directory (for loading resource files)
func WithFixtureDir(path string) Option {
	return func(c *EvalConfig) {
		c.fixtureDir = path
	}
}

// WithContextRoot is an alias for WithFixtureDir for compatibility
func WithContextRoot(path string) Option {
	return WithFixtureDir(path)
}

// WithVerbose enables verbose output
func WithVerbose(enabled bool) Option {
	return func(c *EvalConfig) {
		c.verbose = enabled
	}
}

// WithOutputPath sets the output file path
func WithOutputPath(path string) Option {
	return func(c *EvalConfig) {
		c.outputPath = path
	}
}

// WithLogPath sets the log file path
func WithLogPath(path string) Option {
	return func(c *EvalConfig) {
		c.logPath = path
	}
}

// WithTranscriptDir sets the directory for per-task transcript files
func WithTranscriptDir(path string) Option {
	return func(c *EvalConfig) {
		c.transcriptDir = path
	}
}

// Getters
func (c *EvalConfig) Spec() *models.EvalSpec { return c.spec }
func (c *EvalConfig) SpecDir() string        { return c.specDir }
func (c *EvalConfig) FixtureDir() string     { return c.fixtureDir }
func (c *EvalConfig) ContextRoot() string    { return c.fixtureDir } // Alias for compatibility
func (c *EvalConfig) Verbose() bool          { return c.verbose }
func (c *EvalConfig) OutputPath() string     { return c.outputPath }
func (c *EvalConfig) LogPath() string        { return c.logPath }
func (c *EvalConfig) TranscriptDir() string  { return c.transcriptDir }

// Deprecated: Use EvalConfig instead.
type BenchmarkConfig = EvalConfig

// Deprecated: Use NewEvalConfig instead.
func NewBenchmarkConfig(spec *models.EvalSpec, opts ...Option) *EvalConfig {
	return NewEvalConfig(spec, opts...)
}
