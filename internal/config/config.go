package config

import (
	"github.com/microsoft/waza/internal/models"
)

// BenchmarkConfig is the main configuration with functional options
type BenchmarkConfig struct {
	spec          *models.EvalSpec
	specDir       string // Directory containing the spec file (for resolving test patterns)
	fixtureDir    string // Directory containing fixtures/context files
	verbose       bool
	outputPath    string
	logPath       string
	transcriptDir string // Directory for per-task transcript JSON files
}

// Option is a functional option for BenchmarkConfig
type Option func(*BenchmarkConfig)

// NewBenchmarkConfig creates a new configuration with options
func NewBenchmarkConfig(spec *models.EvalSpec, opts ...Option) *BenchmarkConfig {
	cfg := &BenchmarkConfig{
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
	return func(c *BenchmarkConfig) {
		c.specDir = path
	}
}

// WithFixtureDir sets the fixture directory (for loading resource files)
func WithFixtureDir(path string) Option {
	return func(c *BenchmarkConfig) {
		c.fixtureDir = path
	}
}

// WithContextRoot is an alias for WithFixtureDir for compatibility
func WithContextRoot(path string) Option {
	return WithFixtureDir(path)
}

// WithVerbose enables verbose output
func WithVerbose(enabled bool) Option {
	return func(c *BenchmarkConfig) {
		c.verbose = enabled
	}
}

// WithOutputPath sets the output file path
func WithOutputPath(path string) Option {
	return func(c *BenchmarkConfig) {
		c.outputPath = path
	}
}

// WithLogPath sets the log file path
func WithLogPath(path string) Option {
	return func(c *BenchmarkConfig) {
		c.logPath = path
	}
}

// WithTranscriptDir sets the directory for per-task transcript files
func WithTranscriptDir(path string) Option {
	return func(c *BenchmarkConfig) {
		c.transcriptDir = path
	}
}

// Getters
func (c *BenchmarkConfig) Spec() *models.EvalSpec { return c.spec }
func (c *BenchmarkConfig) SpecDir() string        { return c.specDir }
func (c *BenchmarkConfig) FixtureDir() string     { return c.fixtureDir }
func (c *BenchmarkConfig) ContextRoot() string    { return c.fixtureDir } // Alias for compatibility
func (c *BenchmarkConfig) Verbose() bool          { return c.verbose }
func (c *BenchmarkConfig) OutputPath() string     { return c.outputPath }
func (c *BenchmarkConfig) LogPath() string        { return c.logPath }
func (c *BenchmarkConfig) TranscriptDir() string  { return c.transcriptDir }
