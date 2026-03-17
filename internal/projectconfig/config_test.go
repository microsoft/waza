package projectconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew_ReturnsAllDefaults(t *testing.T) {
	cfg := New()

	// Paths
	assertEqual(t, "Paths.Skills", "skills/", cfg.Paths.Skills)
	assertEqual(t, "Paths.Evals", "evals/", cfg.Paths.Evals)
	assertEqual(t, "Paths.Results", "results/", cfg.Paths.Results)

	// Defaults
	assertEqual(t, "Defaults.Engine", "copilot-sdk", cfg.Defaults.Engine)
	assertEqual(t, "Defaults.Model", "claude-sonnet-4.6", cfg.Defaults.Model)
	assertEqual(t, "Defaults.JudgeModel", "", cfg.Defaults.JudgeModel)
	assertEqualInt(t, "Defaults.Timeout", 300, cfg.Defaults.Timeout)
	assertBoolPtr(t, "Defaults.Parallel", false, cfg.Defaults.Parallel)
	assertEqualInt(t, "Defaults.Workers", 4, cfg.Defaults.Workers)
	assertBoolPtr(t, "Defaults.Verbose", false, cfg.Defaults.Verbose)
	assertBoolPtr(t, "Defaults.SessionLog", false, cfg.Defaults.SessionLog)

	// Cache
	assertBoolPtr(t, "Cache.Enabled", false, cfg.Cache.Enabled)
	assertEqual(t, "Cache.Dir", ".waza-cache", cfg.Cache.Dir)

	// Server
	assertEqualInt(t, "Server.Port", 3000, cfg.Server.Port)
	assertEqual(t, "Server.ResultsDir", "results/", cfg.Server.ResultsDir)

	// Dev
	assertEqual(t, "Dev.Model", "claude-sonnet-4-20250514", cfg.Dev.Model)
	assertEqual(t, "Dev.Target", "medium-high", cfg.Dev.Target)
	assertEqualInt(t, "Dev.MaxIterations", 5, cfg.Dev.MaxIterations)

	// Tokens
	assertEqualInt(t, "Tokens.WarningThreshold", 500, cfg.Tokens.WarningThreshold)
	assertEqualInt(t, "Tokens.FallbackLimit", 1000, cfg.Tokens.FallbackLimit)
	if cfg.Tokens.Limits != nil {
		t.Error("Tokens.Limits should be nil by default")
	}

	// Graders
	assertEqualInt(t, "Graders.ProgramTimeout", 30, cfg.Graders.ProgramTimeout)
}

func TestLoad_FullConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
paths:
  skills: "custom-skills/"
  evals: "custom-evals/"
  results: "custom-results/"
defaults:
  engine: mock
  model: gpt-4o
  judgeModel: claude-sonnet-4.6
  timeout: 600
  parallel: true
  workers: 8
  verbose: true
  sessionLog: true
cache:
  enabled: true
  dir: ".my-cache"
server:
  port: 8080
  resultsDir: "./output"
dev:
  model: gpt-5
  target: high
  maxIterations: 10
tokens:
  warningThreshold: 5000
  fallbackLimit: 3000
  limits:
    defaults:
      gpt-4o: 4096
    overrides:
      gpt-4o: 8192
graders:
  programTimeout: 60
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Paths.Skills", "custom-skills/", cfg.Paths.Skills)
	assertEqual(t, "Paths.Evals", "custom-evals/", cfg.Paths.Evals)
	assertEqual(t, "Paths.Results", "custom-results/", cfg.Paths.Results)
	assertEqual(t, "Defaults.Engine", "mock", cfg.Defaults.Engine)
	assertEqual(t, "Defaults.Model", "gpt-4o", cfg.Defaults.Model)
	assertEqual(t, "Defaults.JudgeModel", "claude-sonnet-4.6", cfg.Defaults.JudgeModel)
	assertEqualInt(t, "Defaults.Timeout", 600, cfg.Defaults.Timeout)
	assertBoolPtr(t, "Defaults.Parallel", true, cfg.Defaults.Parallel)
	assertEqualInt(t, "Defaults.Workers", 8, cfg.Defaults.Workers)
	assertBoolPtr(t, "Defaults.Verbose", true, cfg.Defaults.Verbose)
	assertBoolPtr(t, "Defaults.SessionLog", true, cfg.Defaults.SessionLog)
	assertBoolPtr(t, "Cache.Enabled", true, cfg.Cache.Enabled)
	assertEqual(t, "Cache.Dir", ".my-cache", cfg.Cache.Dir)
	assertEqualInt(t, "Server.Port", 8080, cfg.Server.Port)
	assertEqual(t, "Server.ResultsDir", "./output", cfg.Server.ResultsDir)
	assertEqual(t, "Dev.Model", "gpt-5", cfg.Dev.Model)
	assertEqual(t, "Dev.Target", "high", cfg.Dev.Target)
	assertEqualInt(t, "Dev.MaxIterations", 10, cfg.Dev.MaxIterations)
	assertEqualInt(t, "Tokens.WarningThreshold", 5000, cfg.Tokens.WarningThreshold)
	assertEqualInt(t, "Tokens.FallbackLimit", 3000, cfg.Tokens.FallbackLimit)
	if cfg.Tokens.Limits == nil {
		t.Fatal("Tokens.Limits should not be nil")
	}
	if cfg.Tokens.Limits.Defaults["gpt-4o"] != 4096 {
		t.Errorf("Tokens.Limits.Defaults[gpt-4o] = %d, want 4096", cfg.Tokens.Limits.Defaults["gpt-4o"])
	}
	if cfg.Tokens.Limits.Overrides["gpt-4o"] != 8192 {
		t.Errorf("Tokens.Limits.Overrides[gpt-4o] = %d, want 8192", cfg.Tokens.Limits.Overrides["gpt-4o"])
	}
	assertEqualInt(t, "Graders.ProgramTimeout", 60, cfg.Graders.ProgramTimeout)
}

func TestLoad_PartialConfig_LegacyTwoField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
defaults:
  engine: mock
  model: gpt-4o-mini
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Overridden
	assertEqual(t, "Defaults.Engine", "mock", cfg.Defaults.Engine)
	assertEqual(t, "Defaults.Model", "gpt-4o-mini", cfg.Defaults.Model)

	// Defaults preserved
	assertEqual(t, "Paths.Skills", "skills/", cfg.Paths.Skills)
	assertEqualInt(t, "Defaults.Timeout", 300, cfg.Defaults.Timeout)
	assertBoolPtr(t, "Defaults.Parallel", false, cfg.Defaults.Parallel)
	assertEqualInt(t, "Server.Port", 3000, cfg.Server.Port)
	assertEqualInt(t, "Graders.ProgramTimeout", 30, cfg.Graders.ProgramTimeout)
}

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should be identical to New()
	defaults := New()
	assertEqual(t, "Defaults.Engine", defaults.Defaults.Engine, cfg.Defaults.Engine)
	assertEqual(t, "Defaults.Model", defaults.Defaults.Model, cfg.Defaults.Model)
	assertEqualInt(t, "Defaults.Timeout", defaults.Defaults.Timeout, cfg.Defaults.Timeout)
	assertEqualInt(t, "Server.Port", defaults.Server.Port, cfg.Server.Port)
}

func TestLoad_InvalidYAML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
defaults:
  engine: [not valid yaml
    this is broken
`)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load() should return error for invalid YAML")
	}
}

func TestLoad_WalksUpDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".waza.yaml", `
defaults:
  engine: found-it
`)

	child := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(child)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Dir", root, cfg.Dir)

	assertEqual(t, "Defaults.Engine", "found-it", cfg.Defaults.Engine)
	// Other defaults still populated
	assertEqual(t, "Defaults.Model", "claude-sonnet-4.6", cfg.Defaults.Model)
}

func TestBoolPointerFields(t *testing.T) {
	t.Run("defaults preserved when not set in YAML", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, ".waza.yaml", `
defaults:
  engine: mock
`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		// Parallel not in file → default (false) preserved by merge
		assertBoolPtr(t, "Defaults.Parallel", false, cfg.Defaults.Parallel)
	})

	t.Run("explicitly false", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, ".waza.yaml", `
defaults:
  parallel: false
  verbose: false
cache:
  enabled: false
`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		assertBoolPtr(t, "Defaults.Parallel", false, cfg.Defaults.Parallel)
		assertBoolPtr(t, "Defaults.Verbose", false, cfg.Defaults.Verbose)
		assertBoolPtr(t, "Cache.Enabled", false, cfg.Cache.Enabled)
	})

	t.Run("explicitly true", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, ".waza.yaml", `
defaults:
  parallel: true
  verbose: true
  sessionLog: true
cache:
  enabled: true
`)
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		assertBoolPtr(t, "Defaults.Parallel", true, cfg.Defaults.Parallel)
		assertBoolPtr(t, "Defaults.Verbose", true, cfg.Defaults.Verbose)
		assertBoolPtr(t, "Defaults.SessionLog", true, cfg.Defaults.SessionLog)
		assertBoolPtr(t, "Cache.Enabled", true, cfg.Cache.Enabled)
	})
}

// ========================================
// STORAGE CONFIG TESTS
// ========================================

func TestNew_StorageDefaults(t *testing.T) {
	cfg := New()

	// Storage should have default container name
	assertEqual(t, "Storage.ContainerName", "waza-results", cfg.Storage.ContainerName)
	assertEqual(t, "Storage.Provider", "", cfg.Storage.Provider)
	assertEqual(t, "Storage.AccountName", "", cfg.Storage.AccountName)
	if cfg.Storage.Enabled {
		t.Error("Storage.Enabled should default to false")
	}
}

func TestLoad_StorageConfig_WithAzureBlob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
storage:
  provider: azure-blob
  accountName: myaccount
  containerName: my-results
  enabled: true
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Storage.Provider", "azure-blob", cfg.Storage.Provider)
	assertEqual(t, "Storage.AccountName", "myaccount", cfg.Storage.AccountName)
	assertEqual(t, "Storage.ContainerName", "my-results", cfg.Storage.ContainerName)
	if !cfg.Storage.Enabled {
		t.Error("Storage.Enabled should be true")
	}
}

func TestLoad_StorageConfig_WithoutSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
defaults:
  model: gpt-4o
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Should have default container name even without storage section
	assertEqual(t, "Storage.ContainerName", "waza-results", cfg.Storage.ContainerName)
	assertEqual(t, "Storage.Provider", "", cfg.Storage.Provider)
	if cfg.Storage.Enabled {
		t.Error("Storage.Enabled should default to false when section is missing")
	}
}

func TestLoad_StorageConfig_PartialConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
storage:
  provider: azure-blob
  enabled: true
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Storage.Provider", "azure-blob", cfg.Storage.Provider)
	if !cfg.Storage.Enabled {
		t.Error("Storage.Enabled should be true")
	}
	// ContainerName should fall back to default
	assertEqual(t, "Storage.ContainerName", "waza-results", cfg.Storage.ContainerName)
	// AccountName not specified, should be empty
	assertEqual(t, "Storage.AccountName", "", cfg.Storage.AccountName)
}

func TestLoad_StorageConfig_Disabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
storage:
  provider: azure-blob
  accountName: myaccount
  enabled: false
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Storage.Provider", "azure-blob", cfg.Storage.Provider)
	if cfg.Storage.Enabled {
		t.Error("Storage.Enabled should be false")
	}
}

func TestLoad_StorageConfig_CustomContainerName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
storage:
  containerName: custom-container-123
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	assertEqual(t, "Storage.ContainerName", "custom-container-123", cfg.Storage.ContainerName)
}

func TestLoad_StorageConfig_MergeBehavior(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".waza.yaml", `
storage:
  provider: azure-blob
  # containerName not specified, should use default
  enabled: true
`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Provider from file
	assertEqual(t, "Storage.Provider", "azure-blob", cfg.Storage.Provider)
	// Enabled from file
	if !cfg.Storage.Enabled {
		t.Error("Storage.Enabled should be true")
	}
	// ContainerName from default (not specified in file)
	assertEqual(t, "Storage.ContainerName", "waza-results", cfg.Storage.ContainerName)
}

// --- test helpers ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertEqual(t *testing.T, field, want, got string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", field, got, want)
	}
}

func assertEqualInt(t *testing.T, field string, want, got int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", field, got, want)
	}
}

func assertBoolPtr(t *testing.T, field string, want bool, got *bool) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil, want *%v", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %v, want %v", field, *got, want)
	}
}
