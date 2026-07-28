// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAppendGraderRefCreatesGraders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	seed := "name: my-eval\nversion: 1\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	err := AppendGraderRef(path, GraderRefEntry{
		Ref:    "github.com/waza-evals/fact#factuality@v1.0.0",
		Name:   "factuality_strict",
		Weight: 2.0,
		Config: map[string]any{"threshold": 0.9},
	})
	if err != nil {
		t.Fatalf("AppendGraderRef: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Verify structural correctness with a second parse.
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("re-parse: %v\n---\n%s", err, data)
	}
	graders, ok := doc["graders"].([]any)
	if !ok || len(graders) != 1 {
		t.Fatalf("expected 1 grader, got %#v", doc["graders"])
	}
	g, ok := graders[0].(map[string]any)
	if !ok {
		t.Fatalf("grader entry not a map: %#v", graders[0])
	}
	if g["ref"] != "github.com/waza-evals/fact#factuality@v1.0.0" {
		t.Errorf("ref not written: %v", g["ref"])
	}
	if g["name"] != "factuality_strict" {
		t.Errorf("name not written: %v", g["name"])
	}
	if cfg, ok := g["config"].(map[string]any); !ok || cfg["threshold"] != 0.9 {
		t.Errorf("config not written: %v", g["config"])
	}
	// Sanity: seed content preserved.
	if !strings.Contains(string(data), "name: my-eval") {
		t.Errorf("original content lost:\n%s", data)
	}
}

func TestAppendGraderRefMissingFile(t *testing.T) {
	err := AppendGraderRef(filepath.Join(t.TempDir(), "missing.yaml"), GraderRefEntry{Ref: "x"})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseSetFlag(t *testing.T) {
	got, err := ParseSetFlag([]string{
		"config.threshold=0.9",
		"config.mode=rubric",
		"weight=2",
		"enabled=true",
	})
	if err != nil {
		t.Fatalf("ParseSetFlag: %v", err)
	}
	cfg, ok := got["config"].(map[string]any)
	if !ok {
		t.Fatalf("config not nested: %#v", got)
	}
	if cfg["threshold"] != 0.9 {
		t.Errorf("threshold: %v", cfg["threshold"])
	}
	if cfg["mode"] != "rubric" {
		t.Errorf("mode: %v", cfg["mode"])
	}
	if got["weight"] != int64(2) {
		t.Errorf("weight: %v", got["weight"])
	}
	if got["enabled"] != true {
		t.Errorf("enabled: %v", got["enabled"])
	}
}

func TestParseSetFlagInvalid(t *testing.T) {
	if _, err := ParseSetFlag([]string{"no-equals"}); err == nil {
		t.Error("expected error for missing '='")
	}
	if _, err := ParseSetFlag([]string{"=value"}); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestLockFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFileName)

	lf, err := LoadLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lf.SchemaVersion != LockSchemaVersion {
		t.Errorf("schema: %d", lf.SchemaVersion)
	}

	added := lf.Upsert(LockEntry{
		Ref:     "github.com/waza-evals/fact#factuality@v1.0.0",
		Module:  "github.com/waza-evals/fact",
		Version: "v1.0.0",
	})
	if !added {
		t.Error("expected new entry to be added")
	}
	if err := lf.Save(path); err != nil {
		t.Fatal(err)
	}

	// Re-load and upsert same ref → should replace, not append.
	lf2, err := LoadLockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lf2.Modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(lf2.Modules))
	}
	added = lf2.Upsert(LockEntry{
		Ref:     "github.com/waza-evals/fact#factuality@v1.0.0",
		Module:  "github.com/waza-evals/fact",
		Version: "v1.0.1",
	})
	if added {
		t.Error("expected replace, got new insert")
	}
	if lf2.Modules[0].Version != "v1.0.1" {
		t.Errorf("version: %s", lf2.Modules[0].Version)
	}
}
