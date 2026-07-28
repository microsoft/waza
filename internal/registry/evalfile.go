// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// GraderRefEntry describes the minimal information needed to append a
// remote grader reference to an existing eval.yaml (design §7).
type GraderRefEntry struct {
	Ref    string
	Name   string
	Weight float64
	// Config holds arbitrary key/value overrides supplied via
	// `--set key=value`. Nested keys use dot notation.
	Config map[string]any
}

// ErrEvalFileMissing is returned when the target eval.yaml does not exist.
var ErrEvalFileMissing = errors.New("eval file not found")

// AppendGraderRef parses evalPath, appends a new grader entry using the
// `ref:` short-form, and writes the file back. It preserves the caller's
// formatting for the surrounding YAML by round-tripping through
// yaml.Node.
func AppendGraderRef(evalPath string, entry GraderRefEntry) error {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrEvalFileMissing, evalPath)
		}
		return fmt.Errorf("reading %s: %w", evalPath, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parsing %s: %w", evalPath, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("%s: unexpected YAML shape", evalPath)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top-level must be a mapping", evalPath)
	}

	graders := findOrCreateSequence(doc, "graders")
	graderNode, err := buildGraderRefNode(entry)
	if err != nil {
		return err
	}
	graders.Content = append(graders.Content, graderNode)

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", evalPath, err)
	}
	if err := os.WriteFile(evalPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", evalPath, err)
	}
	return nil
}

// findOrCreateSequence returns the child sequence under key `name`,
// creating one if it doesn't yet exist.
func findOrCreateSequence(mapping *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if k.Value == name {
			if v.Kind != yaml.SequenceNode {
				// Overwrite non-sequence with a fresh sequence.
				v.Kind = yaml.SequenceNode
				v.Tag = "!!seq"
				v.Value = ""
				v.Content = nil
			}
			return v
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapping.Content = append(mapping.Content, keyNode, seq)
	return seq
}

func buildGraderRefNode(entry GraderRefEntry) (*yaml.Node, error) {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMapString(m, "ref", entry.Ref)
	if entry.Name != "" {
		setMapString(m, "name", entry.Name)
	}
	if entry.Weight != 0 {
		setMapFloat(m, "weight", entry.Weight)
	}
	if len(entry.Config) > 0 {
		cfg, err := mapToYAMLNode(entry.Config)
		if err != nil {
			return nil, err
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "config"},
			cfg,
		)
	}
	return m, nil
}

func setMapString(m *yaml.Node, key, val string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val},
	)
}

func setMapFloat(m *yaml.Node, key string, val float64) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(val, 'g', -1, 64)},
	)
}

func mapToYAMLNode(m map[string]any) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for k, v := range m {
		valNode, err := valueToYAMLNode(v)
		if err != nil {
			return nil, err
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
			valNode,
		)
	}
	return node, nil
}

func valueToYAMLNode(v any) (*yaml.Node, error) {
	switch val := v.(type) {
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(val)}, nil
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(val)}, nil
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(val, 10)}, nil
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(val, 'g', -1, 64)}, nil
	case map[string]any:
		return mapToYAMLNode(val)
	default:
		return nil, fmt.Errorf("unsupported --set value type %T", v)
	}
}

// ParseSetFlag parses --set key.path=value inputs into a nested map. It
// mirrors kubectl/helm conventions: the last `=` separates key and value,
// dots in the key segment describe map nesting.
func ParseSetFlag(inputs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, in := range inputs {
		eq := strings.Index(in, "=")
		if eq < 0 {
			return nil, fmt.Errorf("--set %q: expected key=value", in)
		}
		key := strings.TrimSpace(in[:eq])
		val := strings.TrimSpace(in[eq+1:])
		if key == "" {
			return nil, fmt.Errorf("--set %q: empty key", in)
		}
		parts := strings.Split(key, ".")
		insertNested(out, parts, coerceScalar(val))
	}
	return out, nil
}

func insertNested(m map[string]any, keys []string, val any) {
	for i := 0; i < len(keys)-1; i++ {
		k := keys[i]
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
	m[keys[len(keys)-1]] = val
}

// coerceScalar tries a small set of common scalar conversions before
// falling back to string.
func coerceScalar(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
