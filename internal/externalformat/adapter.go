// Package externalformat converts explicitly selected external definitions.
// Admission and conversion operate only on supplied bytes, without execution.
package externalformat

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/models"
	"gopkg.in/yaml.v3"
)

const (
	Format   = "agentv"
	Version  = "5.3.3"
	Profile  = "contains-v1"
	MaxBytes = 1 << 20
	MaxTasks = 1000
	maxDepth = 32
	maxNodes = 100000
)

// Selection is deliberately separate from an evaluation's authored version.
type Selection struct {
	Format  string
	Version string
	Profile string
}

// TaskFile is a native task definition, not an instruction to write a file.
type TaskFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Suite contains native task bytes and their suite identity. Execution policy
// must be explicitly supplied by the production caller; it is not inferred.
type Suite struct {
	Name  string     `json:"name"`
	Tasks []TaskFile `json:"tasks"`
}

type externalSuite struct {
	Name     string           `yaml:"name"`
	Requires externalRequires `yaml:"requires"`
	Prompts  []string         `yaml:"prompts"`
	Tests    []externalTest   `yaml:"tests"`
}

type externalRequires struct {
	AgentV string `yaml:"agentv"`
}

type externalTest struct {
	ID     string              `yaml:"id"`
	Vars   externalVars        `yaml:"vars"`
	Assert []externalAssertion `yaml:"assert"`
}

type externalVars struct {
	Input string `yaml:"input"`
}

type externalAssertion struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
}

var (
	suiteName = regexp.MustCompile(`^[a-z0-9-]{1,128}$`)
	taskID    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func validateSelection(s Selection) error {
	if s.Format != Format || s.Version != Version || s.Profile != Profile {
		return fmt.Errorf("unsupported external format selection %q/%q/%q; select %s/%s/%s explicitly",
			s.Format, s.Version, s.Profile, Format, Version, Profile)
	}
	return nil
}

// Import validates the whole suite before returning any native tasks. This
// profile supports literal inputs and one binary case-sensitive assertion only.
func Import(data []byte, selection Selection) (*Suite, error) {
	if err := validateSelection(selection); err != nil {
		return nil, err
	}
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	fields, err := mapping(root, "$", "name", "requires", "prompts", "tests")
	if err != nil {
		return nil, err
	}
	name, err := stringValue(fields["name"], "$.name")
	if err != nil {
		return nil, err
	}
	if !suiteName.MatchString(name) {
		return nil, fmt.Errorf("$.name: use 1-128 lowercase ASCII letters, digits or hyphens")
	}
	required, err := mapping(fields["requires"], "$.requires", "agentv")
	if err != nil {
		return nil, err
	}
	version, err := stringValue(required["agentv"], "$.requires.agentv")
	if err != nil {
		return nil, err
	}
	if version != Version {
		return nil, fmt.Errorf("$.requires.agentv: expected exact pinned version %q, got %q", Version, version)
	}
	prompts, err := sequence(fields["prompts"], "$.prompts", 1, 1)
	if err != nil {
		return nil, err
	}
	prompt, err := stringValue(prompts[0], "$.prompts[0]")
	if err != nil {
		return nil, err
	}
	if prompt != "{{ input }}" {
		return nil, fmt.Errorf("$.prompts[0]: only the exact identity template %q is supported; custom templates and file references require another adapter", "{{ input }}")
	}
	tests, err := sequence(fields["tests"], "$.tests", 1, MaxTasks)
	if err != nil {
		return nil, err
	}
	result := &Suite{Name: name, Tasks: make([]TaskFile, 0, len(tests))}
	canonical := externalSuite{Name: name, Requires: externalRequires{AgentV: Version},
		Prompts: []string{"{{ input }}"}}
	seen := make(map[string]bool)
	var diagnostics []error
	var nativeBytes int
	for i, node := range tests {
		test, err := readExternalTest(node, fmt.Sprintf("$.tests[%d]", i))
		if err != nil {
			diagnostics = append(diagnostics, err)
			continue
		}
		if seen[test.ID] {
			diagnostics = append(diagnostics, fmt.Errorf("$.tests[%d].id: duplicate task identity %q", i, test.ID))
			continue
		}
		seen[test.ID] = true
		content, err := yaml.Marshal(nativeTask(test))
		if err != nil {
			return nil, fmt.Errorf("serializing native task %q: %w", test.ID, err)
		}
		nativeBytes += len(content)
		if nativeBytes > MaxBytes {
			return nil, fmt.Errorf("definition: converted native tasks exceed %d bytes", MaxBytes)
		}
		canonical.Tests = append(canonical.Tests, test)
		result.Tasks = append(result.Tasks, TaskFile{Path: taskPath(i), Content: string(content)})
	}
	if err := errors.Join(diagnostics...); err != nil {
		return nil, err
	}
	normalized, err := yaml.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("serializing canonical external definition: %w", err)
	}
	if len(normalized) > MaxBytes {
		return nil, fmt.Errorf("definition: canonical external form exceeds %d bytes", MaxBytes)
	}
	return result, nil
}

func readExternalTest(node *yaml.Node, path string) (externalTest, error) {
	fields, err := mapping(node, path, "id", "vars", "assert")
	if err != nil {
		return externalTest{}, err
	}
	id, err := stringValue(fields["id"], path+".id")
	if err != nil {
		return externalTest{}, err
	}
	if !taskID.MatchString(id) {
		return externalTest{}, fmt.Errorf("%s.id: use 1-128 ASCII letters, digits, underscores or hyphens; paths are not task identities", path)
	}
	vars, err := mapping(fields["vars"], path+".vars", "input")
	if err != nil {
		return externalTest{}, err
	}
	input, err := stringValue(vars["input"], path+".vars.input")
	if err != nil {
		return externalTest{}, err
	}
	if strings.TrimSpace(input) == "" || hasTemplate(input) || strings.HasPrefix(strings.TrimSpace(input), "file:") {
		return externalTest{}, fmt.Errorf("%s.vars.input: requires nonblank literal text; templates and file references are unsupported", path)
	}
	assertions, err := sequence(fields["assert"], path+".assert", 1, 1)
	if err != nil {
		return externalTest{}, err
	}
	assertion, err := mapping(assertions[0], path+".assert[0]", "type", "value")
	if err != nil {
		return externalTest{}, err
	}
	kind, err := stringValue(assertion["type"], path+".assert[0].type")
	if err != nil {
		return externalTest{}, err
	}
	if kind != "contains" {
		return externalTest{}, fmt.Errorf("%s.assert[0].type: unsupported assertion %q; only contains is supported, never substitute another grader", path, kind)
	}
	value, err := stringValue(assertion["value"], path+".assert[0].value")
	if err != nil {
		return externalTest{}, err
	}
	if value == "" || hasTemplate(value) || strings.HasPrefix(strings.TrimSpace(value), "file:") {
		return externalTest{}, fmt.Errorf("%s.assert[0].value: requires a nonempty literal substring without template expressions", path)
	}
	return externalTest{ID: id, Vars: externalVars{Input: input},
		Assert: []externalAssertion{{Type: kind, Value: value}}}, nil
}

func hasTemplate(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "{%") || strings.Contains(value, "{#")
}

func nativeTask(test externalTest) models.TestCase {
	return models.TestCase{
		TestID: test.ID, DisplayName: test.ID,
		Stimulus: models.TaskStimulus{Message: test.Vars.Input},
		Validators: []models.ValidatorInline{{
			Identifier: "contains", Kind: models.GraderKindText,
			Parameters: models.TextGraderParameters{ContainsCS: []string{test.Assert[0].Value}},
		}},
	}
}

func taskPath(index int) string {
	return fmt.Sprintf("tasks/%04d.yaml", index+1)
}

// Export reverses exactly the native task subset emitted by Import, including
// edited literal inputs/identities/substring values. Other native fields fail.
// It exports definitions, not execution bindings, results, or evidence.
func Export(suite Suite, selection Selection) ([]byte, error) {
	if err := validateSelection(selection); err != nil {
		return nil, err
	}
	if !suiteName.MatchString(suite.Name) || len(suite.Tasks) < 1 || len(suite.Tasks) > MaxTasks {
		return nil, fmt.Errorf("native suite: requires a supported name and 1-%d tasks", MaxTasks)
	}
	external := externalSuite{Name: suite.Name, Requires: externalRequires{AgentV: Version},
		Prompts: []string{"{{ input }}"}, Tests: make([]externalTest, 0, len(suite.Tasks))}
	var total int
	for i, file := range suite.Tasks {
		total += len(file.Content)
		if total > MaxBytes {
			return nil, fmt.Errorf("native suite: task content exceeds %d bytes", MaxBytes)
		}
		if file.Path != taskPath(i) {
			return nil, fmt.Errorf("native tasks[%d].path: expected exact relative path %q, got %q", i, taskPath(i), file.Path)
		}
		test, err := readNativeTask([]byte(file.Content))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Path, err)
		}
		external.Tests = append(external.Tests, test)
	}
	data, err := yaml.Marshal(external)
	if err != nil {
		return nil, fmt.Errorf("serializing AgentV definition: %w", err)
	}
	if _, err := Import(data, selection); err != nil {
		return nil, fmt.Errorf("exported definition is outside supported profile: %w", err)
	}
	return data, nil
}

func readNativeTask(data []byte) (externalTest, error) {
	root, err := parse(data)
	if err != nil {
		return externalTest{}, err
	}
	fields, err := mapping(root, "$", "id", "name", "inputs", "graders")
	if err != nil {
		return externalTest{}, err
	}
	id, err := stringValue(fields["id"], "$.id")
	if err != nil {
		return externalTest{}, err
	}
	name, err := stringValue(fields["name"], "$.name")
	if err != nil {
		return externalTest{}, err
	}
	if id != name {
		return externalTest{}, fmt.Errorf("$.name: must equal id; separate display names are not representable in this profile")
	}
	inputs, err := mapping(fields["inputs"], "$.inputs", "prompt")
	if err != nil {
		return externalTest{}, err
	}
	input, err := stringValue(inputs["prompt"], "$.inputs.prompt")
	if err != nil {
		return externalTest{}, err
	}
	graders, err := sequence(fields["graders"], "$.graders", 1, 1)
	if err != nil {
		return externalTest{}, err
	}
	grader, err := mapping(graders[0], "$.graders[0]", "name", "type", "config")
	if err != nil {
		return externalTest{}, err
	}
	for field, expected := range map[string]string{"name": "contains", "type": "text"} {
		value, err := stringValue(grader[field], "$.graders[0]."+field)
		if err != nil {
			return externalTest{}, err
		}
		if value != expected {
			return externalTest{}, fmt.Errorf("$.graders[0].%s: only %q is supported", field, expected)
		}
	}
	config, err := mapping(grader["config"], "$.graders[0].config", "contains_cs")
	if err != nil {
		return externalTest{}, err
	}
	values, err := sequence(config["contains_cs"], "$.graders[0].config.contains_cs", 1, 1)
	if err != nil {
		return externalTest{}, err
	}
	value, err := stringValue(values[0], "$.graders[0].config.contains_cs[0]")
	if err != nil {
		return externalTest{}, err
	}
	return externalTest{ID: id, Vars: externalVars{Input: input},
		Assert: []externalAssertion{{Type: "contains", Value: value}}}, nil
}

func parse(data []byte) (*yaml.Node, error) {
	if len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("definition: requires 1-%d bytes of valid UTF-8", MaxBytes)
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("definition: invalid YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("definition: requires exactly one YAML document")
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("definition: requires one nonnull mapping")
	}
	budget := maxNodes
	if err := structure(document.Content[0], "$", 0, &budget); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func structure(node *yaml.Node, path string, depth int, budget *int) error {
	*budget--
	if depth > maxDepth || *budget < 0 {
		return fmt.Errorf("%s: definition exceeds structural limits", path)
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return fmt.Errorf("%s: YAML anchors, aliases and merges are unsupported", path)
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return fmt.Errorf("%s: custom YAML tags are unsupported", path)
		}
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || key.Anchor != "" || seen[key.Value] {
				return fmt.Errorf("%s: requires unique literal string keys, without merges or anchors", path)
			}
			seen[key.Value] = true
			if err := structure(value, path+"."+key.Value, depth+1, budget); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return fmt.Errorf("%s: custom YAML tags are unsupported", path)
		}
		for i, child := range node.Content {
			if err := structure(child, fmt.Sprintf("%s[%d]", path, i), depth+1, budget); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return fmt.Errorf("%s: this profile accepts literal strings only, not null, numbers, booleans or custom tags", path)
		}
	default:
		return fmt.Errorf("%s: unsupported YAML node", path)
	}
	return nil
}

func mapping(node *yaml.Node, path string, required ...string) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: expected a mapping; file references and executable definitions are unsupported", path)
	}
	allowed := make(map[string]bool, len(required))
	for _, name := range required {
		allowed[name] = true
	}
	fields := make(map[string]*yaml.Node, len(required))
	var diagnostics []error
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		if !allowed[name] {
			diagnostics = append(diagnostics, fmt.Errorf("%s.%s: unsupported field; conversion would lose semantics; use a compatible definition or a dedicated adapter, not stripped requirements", path, name))
		}
		fields[name] = node.Content[i+1]
	}
	for _, name := range required {
		if fields[name] == nil {
			diagnostics = append(diagnostics, fmt.Errorf("%s.%s: required by %s", path, name, Profile))
		}
	}
	if err := errors.Join(diagnostics...); err != nil {
		return nil, err
	}
	return fields, nil
}

func sequence(node *yaml.Node, path string, minimum, maximum int) ([]*yaml.Node, error) {
	if node.Kind != yaml.SequenceNode || len(node.Content) < minimum || len(node.Content) > maximum {
		return nil, fmt.Errorf("%s: expected a sequence with %d-%d items", path, minimum, maximum)
	}
	return node.Content, nil
}

func stringValue(node *yaml.Node, path string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s: expected a literal string", path)
	}
	return node.Value, nil
}
