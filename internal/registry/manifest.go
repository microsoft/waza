// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ManifestFileName is the name of the Waza module manifest at the repo root.
const ManifestFileName = "waza.registry.yaml"

// Manifest is a minimal Phase 1 view of waza.registry.yaml. Only the fields
// needed to locate an exported grader preset are decoded; unknown fields are
// ignored so that future manifest additions do not break older Waza clients.
type Manifest struct {
	SchemaVersion int             `yaml:"schema_version"`
	Module        string          `yaml:"module"`
	Description   string          `yaml:"description,omitempty"`
	License       string          `yaml:"license,omitempty"`
	Exports       ManifestExports `yaml:"exports,omitempty"`
}

// ManifestExports holds the exported artifact tables. Phase 1 only reads
// graders.
type ManifestExports struct {
	Graders map[string]ManifestExport `yaml:"graders,omitempty"`
}

// ManifestExport describes one exported artifact. Only Path is required in
// Phase 1; the resolver reads the file at that path relative to the module root.
type ManifestExport struct {
	Path        string   `yaml:"path"`
	Description string   `yaml:"description,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
}

// ParseManifest parses a waza.registry.yaml file.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing waza.registry.yaml: %w", err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = 1
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("waza.registry.yaml: unsupported schema_version %d (expected 1)", m.SchemaVersion)
	}
	return &m, nil
}

// ResolveGraderPath returns the manifest-relative path to the grader preset
// selected by the ref. When the ref uses "#export" syntax, the export is looked
// up in the manifest; when the ref uses a subpath, the path is used directly
// (with a ".yaml" suffix appended when missing).
func (m *Manifest) ResolveGraderPath(ref Ref) (string, error) {
	if ref.Export != "" {
		if m == nil {
			return "", fmt.Errorf("ref %q uses #export syntax but no waza.registry.yaml was found in the module", ref.Raw)
		}
		exp, ok := m.Exports.Graders[ref.Export]
		if !ok {
			return "", fmt.Errorf("ref %q: export %q not found in module manifest (available: %v)", ref.Raw, ref.Export, sortedGraderNames(m))
		}
		if exp.Path == "" {
			return "", fmt.Errorf("ref %q: export %q has empty path in manifest", ref.Raw, ref.Export)
		}
		return exp.Path, nil
	}
	if ref.Path == "" {
		return "", fmt.Errorf("ref %q: must specify either an export (#name) or a subpath", ref.Raw)
	}
	p := ref.Path
	// Users can write either "graders/factuality" or "graders/factuality.yaml".
	if !hasYAMLSuffix(p) {
		p += ".yaml"
	}
	return p, nil
}

func hasYAMLSuffix(p string) bool {
	return len(p) >= 5 && (p[len(p)-5:] == ".yaml" || p[len(p)-4:] == ".yml")
}

func sortedGraderNames(m *Manifest) []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.Exports.Graders))
	for n := range m.Exports.Graders {
		names = append(names, n)
	}
	return names
}
