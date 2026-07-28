// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestExpandGraderConfig_Basic(t *testing.T) {
	ref, err := ParseRef("github.com/o/r#g@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	preset := []byte(`type: text
name: factuality
weight: 1
config:
  contains:
    - alpha
    - beta
`)
	resolved := &ResolvedGrader{Ref: ref, PresetYAML: preset, Lock: LockModule{Ref: ref.Raw}}

	local := models.GraderConfig{Ref: ref.Raw}
	got, err := ExpandGraderConfig(local, resolved)
	if err != nil {
		t.Fatalf("ExpandGraderConfig: %v", err)
	}
	if got.Kind != models.GraderKindText {
		t.Fatalf("Kind = %q, want text", got.Kind)
	}
	if got.Identifier != "factuality" {
		t.Fatalf("Identifier = %q, want factuality", got.Identifier)
	}
	if got.Ref != ref.Raw {
		t.Fatalf("Ref not preserved: %q", got.Ref)
	}
	params, ok := got.Parameters.(models.TextGraderParameters)
	if !ok {
		t.Fatalf("Parameters = %T, want TextGraderParameters", got.Parameters)
	}
	if len(params.Contains) != 2 || params.Contains[0] != "alpha" {
		t.Fatalf("Contains = %v", params.Contains)
	}
}

func TestExpandGraderConfig_LocalScalarOverride(t *testing.T) {
	ref, _ := ParseRef("github.com/o/r#g@v1.0.0")
	preset := []byte("type: text\nname: preset-name\nweight: 1\n")
	resolved := &ResolvedGrader{Ref: ref, PresetYAML: preset, Lock: LockModule{Ref: ref.Raw}}

	local := models.GraderConfig{
		Ref:        ref.Raw,
		Identifier: "my-name",
		Weight:     5,
	}
	got, err := ExpandGraderConfig(local, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identifier != "my-name" {
		t.Fatalf("Identifier = %q, want my-name", got.Identifier)
	}
	if got.Weight != 5 {
		t.Fatalf("Weight = %v, want 5", got.Weight)
	}
}

func TestExpandGraderConfig_ConfigDeepMerge(t *testing.T) {
	ref, _ := ParseRef("github.com/o/r#g@v1.0.0")
	preset := []byte("type: text\nname: g\nconfig:\n  contains: [a, b]\n  not_contains: [x]\n")
	resolved := &ResolvedGrader{Ref: ref, PresetYAML: preset, Lock: LockModule{Ref: ref.Raw}}

	local := models.GraderConfig{
		Ref: ref.Raw,
		// Override list should REPLACE the remote list.
		Parameters: models.GenericGraderParameters{
			"contains": []any{"z"},
		},
	}
	got, err := ExpandGraderConfig(local, resolved)
	if err != nil {
		t.Fatal(err)
	}
	params, ok := got.Parameters.(models.TextGraderParameters)
	if !ok {
		t.Fatalf("Parameters = %T, want TextGraderParameters", got.Parameters)
	}
	if len(params.Contains) != 1 || params.Contains[0] != "z" {
		t.Fatalf("Contains = %v, want [z]", params.Contains)
	}
	if len(params.NotContains) != 1 || params.NotContains[0] != "x" {
		t.Fatalf("NotContains = %v, want [x] (preserved from remote)", params.NotContains)
	}
}

func TestExpandGraderConfig_MissingType(t *testing.T) {
	ref, _ := ParseRef("github.com/o/r#g@v1.0.0")
	preset := []byte("name: bad\n") // no type
	resolved := &ResolvedGrader{Ref: ref, PresetYAML: preset, Lock: LockModule{Ref: ref.Raw}}

	if _, err := ExpandGraderConfig(models.GraderConfig{Ref: ref.Raw}, resolved); err == nil {
		t.Fatal("expected error for preset missing type")
	}
}

func TestExpandGraderConfig_DefaultName(t *testing.T) {
	ref, _ := ParseRef("github.com/o/r#factuality@v1.0.0")
	preset := []byte("type: text\n") // no name
	resolved := &ResolvedGrader{Ref: ref, PresetYAML: preset, Lock: LockModule{Ref: ref.Raw}}

	got, err := ExpandGraderConfig(models.GraderConfig{Ref: ref.Raw}, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got.Identifier != "factuality" {
		t.Fatalf("default name = %q, want factuality", got.Identifier)
	}
}
