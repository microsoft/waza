package config

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestNewBenchmarkConfig_DefaultValues(t *testing.T) {
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "test-spec"}}

	cfg := NewBenchmarkConfig(spec)

	if cfg.Spec() != spec {
		t.Fatalf("Spec() = %p, want %p", cfg.Spec(), spec)
	}
	if cfg.SpecDir() != "" {
		t.Fatalf("SpecDir() = %q, want empty", cfg.SpecDir())
	}
	if cfg.FixtureDir() != "" {
		t.Fatalf("FixtureDir() = %q, want empty", cfg.FixtureDir())
	}
	if cfg.ContextRoot() != "" {
		t.Fatalf("ContextRoot() = %q, want empty", cfg.ContextRoot())
	}
	if cfg.Verbose() {
		t.Fatalf("Verbose() = true, want false")
	}
	if cfg.OutputPath() != "" {
		t.Fatalf("OutputPath() = %q, want empty", cfg.OutputPath())
	}
	if cfg.LogPath() != "" {
		t.Fatalf("LogPath() = %q, want empty", cfg.LogPath())
	}
	if cfg.TranscriptDir() != "" {
		t.Fatalf("TranscriptDir() = %q, want empty", cfg.TranscriptDir())
	}
}

func TestNewBenchmarkConfig_AppliesFunctionalOptions(t *testing.T) {
	spec := &models.EvalSpec{}

	cfg := NewBenchmarkConfig(
		spec,
		WithSpecDir("/tmp/specs"),
		WithFixtureDir("/tmp/fixtures"),
		WithVerbose(true),
		WithOutputPath("results.json"),
		WithLogPath("logs/run.log"),
		WithTranscriptDir("transcripts"),
	)

	if cfg.SpecDir() != "/tmp/specs" {
		t.Fatalf("SpecDir() = %q, want %q", cfg.SpecDir(), "/tmp/specs")
	}
	if cfg.FixtureDir() != "/tmp/fixtures" {
		t.Fatalf("FixtureDir() = %q, want %q", cfg.FixtureDir(), "/tmp/fixtures")
	}
	if cfg.ContextRoot() != "/tmp/fixtures" {
		t.Fatalf("ContextRoot() = %q, want %q", cfg.ContextRoot(), "/tmp/fixtures")
	}
	if !cfg.Verbose() {
		t.Fatalf("Verbose() = false, want true")
	}
	if cfg.OutputPath() != "results.json" {
		t.Fatalf("OutputPath() = %q, want %q", cfg.OutputPath(), "results.json")
	}
	if cfg.LogPath() != "logs/run.log" {
		t.Fatalf("LogPath() = %q, want %q", cfg.LogPath(), "logs/run.log")
	}
	if cfg.TranscriptDir() != "transcripts" {
		t.Fatalf("TranscriptDir() = %q, want %q", cfg.TranscriptDir(), "transcripts")
	}
}

func TestWithContextRoot_Alias(t *testing.T) {
	cfg := NewBenchmarkConfig(&models.EvalSpec{}, WithContextRoot("fixtures"))

	if cfg.FixtureDir() != "fixtures" {
		t.Fatalf("FixtureDir() = %q, want %q", cfg.FixtureDir(), "fixtures")
	}
	if cfg.ContextRoot() != "fixtures" {
		t.Fatalf("ContextRoot() = %q, want %q", cfg.ContextRoot(), "fixtures")
	}
}

func TestOptionOrder_LastOptionWins(t *testing.T) {
	cfg := NewBenchmarkConfig(
		&models.EvalSpec{},
		WithVerbose(true),
		WithVerbose(false),
		WithFixtureDir("first"),
		WithContextRoot("second"),
	)

	if cfg.Verbose() {
		t.Fatalf("Verbose() = true, want false")
	}
	if cfg.FixtureDir() != "second" {
		t.Fatalf("FixtureDir() = %q, want %q", cfg.FixtureDir(), "second")
	}
	if cfg.ContextRoot() != "second" {
		t.Fatalf("ContextRoot() = %q, want %q", cfg.ContextRoot(), "second")
	}
}

func TestNewBenchmarkConfig_NilSpecAllowed(t *testing.T) {
	cfg := NewBenchmarkConfig(nil, WithOutputPath(""), WithLogPath(""), WithTranscriptDir(""))

	if cfg.Spec() != nil {
		t.Fatalf("Spec() = %v, want nil", cfg.Spec())
	}
	if cfg.OutputPath() != "" {
		t.Fatalf("OutputPath() = %q, want empty", cfg.OutputPath())
	}
	if cfg.LogPath() != "" {
		t.Fatalf("LogPath() = %q, want empty", cfg.LogPath())
	}
	if cfg.TranscriptDir() != "" {
		t.Fatalf("TranscriptDir() = %q, want empty", cfg.TranscriptDir())
	}
}

func TestNewBenchmarkConfig_NilOptionPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option, got none")
		}
	}()

	_ = NewBenchmarkConfig(&models.EvalSpec{}, nil)
}
