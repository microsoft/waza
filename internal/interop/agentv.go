// Package interop implements explicitly selected, offline definition projections.
package interop

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const AgentVCommit = "68df6d2206cea61f721d0445b405efcb5e94aff0"
const Adapter = "agentv-" + AgentVCommit + "-contains-v1"
const MaxBytes = 4 << 20

// ImportContains projects one explicitly selected assertion, not its containing
// task or suite. Sibling assertions and execution settings are never converted.
func ImportContains(data []byte, testID string, index int) ([]byte, error) {
	root, err := document(data)
	if err != nil {
		return nil, err
	}
	if testID == "" || index < 1 {
		return nil, fmt.Errorf("select --test and a 1-based --assertion; whole-suite conversion is unsupported")
	}
	if _, err := text(scalar(testID)); err != nil {
		return nil, fmt.Errorf("--test: expected literal identity without interpolation: %w", err)
	}
	tests := field(root, "tests")
	if tests == nil || tests.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("$.tests: expected inline test sequence; external datasets are unsupported")
	}
	var selected *yaml.Node
	for _, test := range tests.Content {
		id := field(test, "id")
		if id != nil && id.Tag == "!!str" && id.Value == testID {
			if selected != nil {
				return nil, fmt.Errorf("$.tests: duplicate selected id %q", testID)
			}
			selected = test
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("$.tests: test %q not found", testID)
	}
	assertions := field(selected, "assert")
	if assertions == nil || assertions.Kind != yaml.SequenceNode || index > len(assertions.Content) {
		return nil, fmt.Errorf("test %q: --assertion %d not found", testID, index)
	}
	assertion := assertions.Content[index-1]
	if err := fields(assertion, "type", "value", "metric"); err != nil {
		return nil, fmt.Errorf("test %q assert[%d]: %w", testID, index-1, err)
	}
	kind, err := text(field(assertion, "type"))
	if err != nil || kind != "contains" {
		return nil, fmt.Errorf("test %q assert[%d].type: only literal case-sensitive contains is supported; no grader substitution", testID, index-1)
	}
	value, err := text(field(assertion, "value"))
	if err != nil || value == "" {
		return nil, fmt.Errorf("test %q assert[%d].value: expected nonempty literal string", testID, index-1)
	}
	name := "agentv-contains"
	if metric := field(assertion, "metric"); metric != nil {
		name, err = text(metric)
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("assert.metric: expected nonblank string")
		}
	}
	grader := mapping(
		yamlEntry{"name", scalar(name)},
		yamlEntry{"type", scalar("text")},
		yamlEntry{"config", mapping(yamlEntry{"contains_cs", sequence(scalar(value))})},
	)
	output, err := yaml.Marshal(grader)
	if err != nil {
		return nil, fmt.Errorf("encoding native grader: %w", err)
	}
	header := fmt.Sprintf("# Unverified source SHA256: %x\n# Adapter: %s\n# Assertion-only projection: test %s, assertion %d; NOT a converted task or suite.\n",
		sha256.Sum256(data), Adapter, strconv.Quote(testID), index)
	return append([]byte(header), output...), nil
}

// ExportContains accepts exactly one native text grader with one contains_cs
// check, refusing additional checks or configuration rather than dropping them.
func ExportContains(data []byte) ([]byte, error) {
	root, err := document(data)
	if err != nil {
		return nil, err
	}
	if err := fields(root, "name", "type", "config"); err != nil {
		return nil, err
	}
	name, err := text(field(root, "name"))
	if err != nil || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("$.name: expected nonblank string")
	}
	kind, err := text(field(root, "type"))
	if err != nil || kind != "text" {
		return nil, fmt.Errorf("$.type: only text with one contains_cs check is exportable")
	}
	config := field(root, "config")
	if err := fields(config, "contains_cs"); err != nil {
		return nil, fmt.Errorf("$.config: %w", err)
	}
	checks := field(config, "contains_cs")
	if checks == nil || checks.Kind != yaml.SequenceNode || len(checks.Content) != 1 {
		return nil, fmt.Errorf("$.config.contains_cs: exactly one check required to preserve binary scores")
	}
	value, err := text(checks.Content[0])
	if err != nil || value == "" {
		return nil, fmt.Errorf("$.config.contains_cs[0]: expected nonempty string")
	}
	result, err := yaml.Marshal(mapping(
		yamlEntry{"metric", scalar(name)},
		yamlEntry{"type", scalar("contains")},
		yamlEntry{"value", scalar(value)},
	))
	if err != nil {
		return nil, fmt.Errorf("encoding AgentV assertion: %w", err)
	}
	return result, nil
}

func document(data []byte) (*yaml.Node, error) {
	if len(data) > MaxBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("input must be UTF-8 and at most %d bytes", MaxBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing definition: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("definition must be a nonnull mapping")
	}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("definition must contain exactly one document")
	}
	if err := safeNode(doc.Content[0], "$", 0); err != nil {
		return nil, err
	}
	return doc.Content[0], nil
}

func safeNode(node *yaml.Node, path string, depth int) error {
	if depth > 64 || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("%s: aliases, anchors and excessive nesting are unsupported", path)
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return fmt.Errorf("%s: unsupported YAML tag %s", path, node.Tag)
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || key.Anchor != "" || seen[key.Value] {
				return fmt.Errorf("%s: unique string keys required; merge keys are unsupported", path)
			}
			seen[key.Value] = true
			if err := safeNode(value, path+"."+key.Value, depth+1); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return fmt.Errorf("%s: unsupported YAML tag %s", path, node.Tag)
		}
		for i, child := range node.Content {
			if err := safeNode(child, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" && node.Tag != "!!bool" && node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!null" {
			return fmt.Errorf("%s: unsupported YAML tag %s", path, node.Tag)
		}
	default:
		return fmt.Errorf("%s: unsupported node", path)
	}
	return nil
}

func fields(node *yaml.Node, names ...string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping")
	}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		allowed := false
		for _, name := range names {
			allowed = allowed || key == name
		}
		if !allowed {
			return fmt.Errorf("unsupported field %q; conversion would lose semantics", key)
		}
	}
	return nil
}

func field(node *yaml.Node, name string) *yaml.Node {
	if node != nil && node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				return node.Content[i+1]
			}
		}
	}
	return nil
}

func text(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("expected string")
	}
	for _, delimiter := range []string{"${", "{{", "}}", "{%", "{#"} {
		if strings.Contains(node.Value, delimiter) {
			return "", fmt.Errorf("interpolation is unsupported; supply a resolved literal")
		}
	}
	return node.Value, nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

type yamlEntry struct {
	name  string
	value *yaml.Node
}

func mapping(entries ...yamlEntry) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, entry := range entries {
		node.Content = append(node.Content, scalar(entry.name), entry.value)
	}
	return node
}

func sequence(nodes ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: nodes}
}
