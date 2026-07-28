// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeEvalYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "eval.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegistryAddAppendsGraderAndWritesLock(t *testing.T) {
	dir := t.TempDir()
	evalPath := writeEvalYAML(t, dir, "name: my-eval\nversion: 1\n")

	cmd := newRegistryCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"add", "github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
		"--name", "factuality_strict",
		"--set", "config.threshold=0.9",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, buf.String())
	}

	data, err := os.ReadFile(evalPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("re-parse eval.yaml: %v\n%s", err, data)
	}
	graders, ok := doc["graders"].([]any)
	if !ok || len(graders) != 1 {
		t.Fatalf("graders sequence missing: %#v", doc["graders"])
	}
	g, ok := graders[0].(map[string]any)
	if !ok {
		t.Fatalf("grader entry not a map: %#v", graders[0])
	}
	if g["ref"] != "github.com/waza-evals/fact#factuality@v1.0.0" {
		t.Errorf("ref: %v", g["ref"])
	}
	if g["name"] != "factuality_strict" {
		t.Errorf("name: %v", g["name"])
	}

	lockPath := filepath.Join(dir, "waza.lock")
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read waza.lock: %v", err)
	}
	if !strings.Contains(string(lockData), "github.com/waza-evals/fact#factuality@v1.0.0") {
		t.Errorf("lock missing ref:\n%s", lockData)
	}
	if !strings.Contains(string(lockData), "schema_version: 1") {
		t.Errorf("lock missing schema_version:\n%s", lockData)
	}
}

func TestRegistryAddDryRun(t *testing.T) {
	dir := t.TempDir()
	evalPath := writeEvalYAML(t, dir, "name: e\n")

	cmd := newRegistryCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{
		"add", "github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
		"--dry-run",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "DRY RUN") {
		t.Errorf("expected DRY RUN in output:\n%s", buf.String())
	}
	// Eval file must be unchanged.
	data, _ := os.ReadFile(evalPath)
	if strings.Contains(string(data), "ref:") {
		t.Errorf("dry run modified eval.yaml:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "waza.lock")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote waza.lock")
	}
}

func TestRegistryAddRejectsBadRef(t *testing.T) {
	cmd := newRegistryCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"add", "./local.yaml"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for local path")
	}
}

func TestRegistryAddRejectsBadSetFlag(t *testing.T) {
	dir := t.TempDir()
	evalPath := writeEvalYAML(t, dir, "name: e\n")
	cmd := newRegistryCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"add", "github.com/waza-evals/fact#factuality@v1.0.0",
		"--eval", evalPath,
		"--set", "malformed",
	})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for malformed --set")
	}
}
