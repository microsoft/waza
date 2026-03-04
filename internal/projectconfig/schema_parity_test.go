package projectconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// schemaJSON mirrors the subset of JSON Schema we need to extract defaults.
type schemaJSON struct {
	Properties map[string]schemaProp `json:"properties"`
}

type schemaProp struct {
	Properties map[string]schemaProp `json:"properties"`
	Default    any                   `json:"default"`
	Type       string                `json:"type"`
}

// repoRoot returns the repository root by walking up from this test file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to determine test file path")
	}
	// thisFile: …/internal/projectconfig/schema_parity_test.go → repo root is 3 levels up
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func TestSchemaDefaultsMatchGoDefaults(t *testing.T) {
	schemaPath := filepath.Join(repoRoot(t), "schemas", "config.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}

	var schema schemaJSON
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parsing schema: %v", err)
	}

	cfg := New()

	// Helper to extract a nested default from the parsed schema.
	getDefault := func(section, field string) any {
		sec, ok := schema.Properties[section]
		if !ok {
			t.Fatalf("schema missing section %q", section)
		}
		f, ok := sec.Properties[field]
		if !ok {
			t.Fatalf("schema section %q missing field %q", section, field)
		}
		return f.Default
	}

	// --- paths ---
	assertStringDefault(t, getDefault("paths", "skills"), cfg.Paths.Skills, "paths.skills")
	assertStringDefault(t, getDefault("paths", "evals"), cfg.Paths.Evals, "paths.evals")
	assertStringDefault(t, getDefault("paths", "results"), cfg.Paths.Results, "paths.results")

	// --- defaults ---
	assertStringDefault(t, getDefault("defaults", "engine"), cfg.Defaults.Engine, "defaults.engine")
	assertStringDefault(t, getDefault("defaults", "model"), cfg.Defaults.Model, "defaults.model")
	assertIntDefault(t, getDefault("defaults", "timeout"), cfg.Defaults.Timeout, "defaults.timeout")
	assertBoolDefault(t, getDefault("defaults", "parallel"), *cfg.Defaults.Parallel, "defaults.parallel")
	assertIntDefault(t, getDefault("defaults", "workers"), cfg.Defaults.Workers, "defaults.workers")
	assertBoolDefault(t, getDefault("defaults", "verbose"), *cfg.Defaults.Verbose, "defaults.verbose")
	assertBoolDefault(t, getDefault("defaults", "sessionLog"), *cfg.Defaults.SessionLog, "defaults.sessionLog")

	// --- cache ---
	assertBoolDefault(t, getDefault("cache", "enabled"), *cfg.Cache.Enabled, "cache.enabled")
	assertStringDefault(t, getDefault("cache", "dir"), cfg.Cache.Dir, "cache.dir")

	// --- server ---
	assertIntDefault(t, getDefault("server", "port"), cfg.Server.Port, "server.port")
	assertStringDefault(t, getDefault("server", "resultsDir"), cfg.Server.ResultsDir, "server.resultsDir")

	// --- dev ---
	assertStringDefault(t, getDefault("dev", "model"), cfg.Dev.Model, "dev.model")
	assertStringDefault(t, getDefault("dev", "target"), cfg.Dev.Target, "dev.target")
	assertIntDefault(t, getDefault("dev", "maxIterations"), cfg.Dev.MaxIterations, "dev.maxIterations")

	// --- tokens ---
	assertIntDefault(t, getDefault("tokens", "warningThreshold"), cfg.Tokens.WarningThreshold, "tokens.warningThreshold")
	assertIntDefault(t, getDefault("tokens", "fallbackLimit"), cfg.Tokens.FallbackLimit, "tokens.fallbackLimit")

	// --- graders ---
	assertIntDefault(t, getDefault("graders", "programTimeout"), cfg.Graders.ProgramTimeout, "graders.programTimeout")

	// --- storage ---
	assertStringDefault(t, getDefault("storage", "provider"), cfg.Storage.Provider, "storage.provider")
	assertStringDefault(t, getDefault("storage", "accountName"), cfg.Storage.AccountName, "storage.accountName")
	assertStringDefault(t, getDefault("storage", "containerName"), cfg.Storage.ContainerName, "storage.containerName")
	assertBoolDefault(t, getDefault("storage", "enabled"), cfg.Storage.Enabled, "storage.enabled")
}

func assertStringDefault(t *testing.T, schemaVal any, goVal, field string) {
	t.Helper()
	s, ok := schemaVal.(string)
	if !ok {
		t.Errorf("%s: schema default is %T(%v), expected string", field, schemaVal, schemaVal)
		return
	}
	if s != goVal {
		t.Errorf("%s: schema default %q != Go default %q", field, s, goVal)
	}
}

func assertIntDefault(t *testing.T, schemaVal any, goVal int, field string) {
	t.Helper()
	// JSON numbers unmarshal as float64.
	f, ok := schemaVal.(float64)
	if !ok {
		t.Errorf("%s: schema default is %T(%v), expected number", field, schemaVal, schemaVal)
		return
	}
	if int(f) != goVal {
		t.Errorf("%s: schema default %d != Go default %d", field, int(f), goVal)
	}
}

func assertBoolDefault(t *testing.T, schemaVal any, goVal bool, field string) {
	t.Helper()
	b, ok := schemaVal.(bool)
	if !ok {
		t.Errorf("%s: schema default is %T(%v), expected bool", field, schemaVal, schemaVal)
		return
	}
	if b != goVal {
		t.Errorf("%s: schema default %v != Go default %v", field, b, goVal)
	}
}
