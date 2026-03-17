package config

import (
	"github.com/microsoft/waza/internal/models"
)

// RunConfig is the main configuration with functional options
type RunConfig struct {
	spec          *models.EvalSpec
	specDir       string // Directory containing the spec file (for resolving test patterns)
	fixtureDir    string // Directory containing fixtures/context files
	verbose       bool
	outputPath    string
	logPath       string
	transcriptDir string // Directory for per-task transcript JSON files
}

// Option is a functional option for RunConfig
type Option func(*RunConfig)

// NewRunConfig creates a new configuration with options
func NewRunConfig(spec *models.EvalSpec, opts ...Option) *RunConfig {
	cfg := &RunConfig{
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
	return func(c *RunConfig) {
		c.specDir = path
	}
}

// WithFixtureDir sets the fixture directory (for loading resource files)
func WithFixtureDir(path string) Option {
	return func(c *RunConfig) {
		c.fixtureDir = path
	}
}

// WithContextRoot is an alias for WithFixtureDir for compatibility
func WithContextRoot(path string) Option {
	return WithFixtureDir(path)
}

// WithVerbose enables verbose output
func WithVerbose(enabled bool) Option {
	return func(c *RunConfig) {
		c.verbose = enabled
	}
}

// WithOutputPath sets the output file path
func WithOutputPath(path string) Option {
	return func(c *RunConfig) {
		c.outputPath = path
	}
}

// WithLogPath sets the log file path
func WithLogPath(path string) Option {
	return func(c *RunConfig) {
		c.logPath = path
	}
}

// WithTranscriptDir sets the directory for per-task transcript files
func WithTranscriptDir(path string) Option {
	return func(c *RunConfig) {
		c.transcriptDir = path
	}
}

// Getters
func (c *RunConfig) Spec() *models.EvalSpec { return c.spec }
func (c *RunConfig) SpecDir() string        { return c.specDir }
func (c *RunConfig) FixtureDir() string     { return c.fixtureDir }
func (c *RunConfig) ContextRoot() string    { return c.fixtureDir } // Alias for compatibility
func (c *RunConfig) Verbose() bool          { return c.verbose }
func (c *RunConfig) OutputPath() string     { return c.outputPath }
func (c *RunConfig) LogPath() string        { return c.logPath }
func (c *RunConfig) TranscriptDir() string  { return c.transcriptDir }
