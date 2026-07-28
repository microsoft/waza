// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"fmt"

	"github.com/microsoft/waza/internal/models"
	"gopkg.in/yaml.v3"
)

// ExpandGraderConfig takes a resolved remote grader-preset and merges it with
// the local override GraderConfig, returning a concrete GraderConfig that can
// flow through the normal grader pipeline.
//
// Merge rules (design doc §7):
//  1. Remote preset is the base; it MUST include "type".
//  2. Local scalar fields override remote scalars when non-zero:
//     name, weight, model, rubric, script.
//  3. Local `config` deep-merges into remote `config`; local wins on key collisions.
//  4. Lists are replaced, not concatenated.
//  5. `ref` is preserved on the expanded config for auditability.
func ExpandGraderConfig(local models.GraderConfig, resolved *ResolvedGrader) (models.GraderConfig, error) {
	if resolved == nil {
		return models.GraderConfig{}, fmt.Errorf("expand: resolved grader is nil")
	}

	// Parse the remote preset YAML as a generic map so we can deep-merge overrides.
	var remote map[string]any
	if err := yaml.Unmarshal(resolved.PresetYAML, &remote); err != nil {
		return models.GraderConfig{}, fmt.Errorf("parsing remote preset for %s: %w", resolved.Ref.Raw, err)
	}
	if remote == nil {
		remote = map[string]any{}
	}
	if _, ok := remote["type"]; !ok {
		return models.GraderConfig{}, fmt.Errorf("remote preset %s is missing required 'type' field", resolved.Ref.Raw)
	}

	// Scalar overrides.
	if local.Identifier != "" {
		remote["name"] = local.Identifier
	}
	if local.Weight > 0 {
		remote["weight"] = local.Weight
	}
	if local.ModelID != "" {
		remote["model"] = local.ModelID
	}
	if local.Rubric != "" {
		remote["rubric"] = local.Rubric
	}
	if local.ScriptPath != "" {
		remote["script"] = local.ScriptPath
	}
	// A local `type` on a ref entry is unusual but permit it (matches design
	// doc note that local scalars win).
	if local.Kind != "" {
		remote["type"] = string(local.Kind)
	}

	// Deep-merge config maps.
	if overrides, ok := local.Parameters.(models.GenericGraderParameters); ok && len(overrides) > 0 {
		remoteCfg, _ := remote["config"].(map[string]any)
		if remoteCfg == nil {
			remoteCfg = map[string]any{}
		}
		mergeMaps(remoteCfg, map[string]any(overrides))
		remote["config"] = remoteCfg
	}

	// Round-trip through YAML so the existing GraderConfig.UnmarshalYAML does
	// the strongly-typed decode + validation for us.
	merged, err := yaml.Marshal(remote)
	if err != nil {
		return models.GraderConfig{}, fmt.Errorf("re-marshaling merged grader for %s: %w", resolved.Ref.Raw, err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(merged, &node); err != nil {
		return models.GraderConfig{}, fmt.Errorf("re-parsing merged grader for %s: %w", resolved.Ref.Raw, err)
	}
	// yaml.Node from Unmarshal is a document node; step into its content.
	target := &node
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		target = node.Content[0]
	}
	var out models.GraderConfig
	if err := out.UnmarshalYAML(target); err != nil {
		return models.GraderConfig{}, fmt.Errorf("expanding ref %s: %w", resolved.Ref.Raw, err)
	}
	// Preserve the ref for downstream auditing / dashboard display.
	out.Ref = resolved.Ref.Raw
	// If the remote preset omitted a name, default to the export or last path
	// segment so grader outputs have a stable identifier.
	if out.Identifier == "" {
		out.Identifier = defaultGraderName(resolved.Ref)
	}
	return out, nil
}

// mergeMaps deep-merges src into dst, mutating dst. Values in src override dst
// on key collisions. Nested maps are merged recursively; slices are replaced.
func mergeMaps(dst, src map[string]any) {
	for k, v := range src {
		if existing, ok := dst[k]; ok {
			if em, ok := existing.(map[string]any); ok {
				if sm, ok := v.(map[string]any); ok {
					mergeMaps(em, sm)
					continue
				}
			}
		}
		dst[k] = v
	}
}

func defaultGraderName(ref Ref) string {
	if ref.Export != "" {
		return ref.Export
	}
	if ref.Path != "" {
		// last segment, stripped of extension
		s := ref.Path
		for i := len(s) - 1; i >= 0; i-- {
			if s[i] == '/' {
				s = s[i+1:]
				break
			}
		}
		for i := len(s) - 1; i >= 0; i-- {
			if s[i] == '.' {
				s = s[:i]
				break
			}
		}
		return s
	}
	return ref.Module()
}
