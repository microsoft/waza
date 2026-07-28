// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistrySearchTable(t *testing.T) {
	cmd := newRegistryCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search", "factuality"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "REF") || !strings.Contains(out, "KIND") {
		t.Errorf("table header missing:\n%s", out)
	}
	if !strings.Contains(out, "factuality") {
		t.Errorf("expected factuality result:\n%s", out)
	}
}

func TestRegistrySearchJSON(t *testing.T) {
	cmd := newRegistryCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search", "factuality", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		t.Fatalf("json decode: %v\n%s", err, buf.String())
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if _, ok := results[0]["ref"]; !ok {
		t.Errorf("missing 'ref' field: %#v", results[0])
	}
}

func TestRegistrySearchKindValidation(t *testing.T) {
	cmd := newRegistryCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"search", "x", "--kind", "bogus"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for bogus --kind")
	}
}

func TestRegistrySearchFormatValidation(t *testing.T) {
	cmd := newRegistryCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"search", "x", "--format", "yaml"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for unsupported --format")
	}
}

func TestRegistrySearchNoQueryOK(t *testing.T) {
	cmd := newRegistryCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"search"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "REF") {
		t.Errorf("expected table output for empty query, got:\n%s", buf.String())
	}
}
