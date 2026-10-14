package models

import (
	"bytes"
	"fmt"
	"io"
	"maps"

	"gopkg.in/yaml.v3"
)

// MockResponseSource identifies a declared matcher without normalizing its
// output presence or numeric YAML tags.
type MockResponseSource struct {
	Kind    string
	Command string
	Server  string
	Tool    string
	Index   int
	Data    []byte
	Finite  bool
}

// MockResponseSources extracts only declared mock response locations. Payload
// properties named responses or sequence are not configuration.
func MockResponseSources(data []byte) ([]MockResponseSource, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("mock source YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("mock source YAML: %w", err)
		}
		return nil, fmt.Errorf("mock source YAML must contain one document")
	}
	if len(document.Content) != 1 {
		return nil, fmt.Errorf("mock source YAML must contain one mapping")
	}
	root := document.Content[0]
	var sources []MockResponseSource
	appendResponses := func(node *yaml.Node, identity MockResponseSource) error {
		if node == nil {
			return nil
		}
		node, err := detachedFaultNode(node)
		if err != nil {
			return err
		}
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("mock responses must be a sequence")
		}
		for i, response := range node.Content {
			encoded, err := yaml.Marshal(response)
			if err != nil {
				return fmt.Errorf("marshal mock response source: %w", err)
			}
			identity.Index, identity.Data = i, encoded
			identity.Finite = faultNodeHasKey(response, "sequence")
			sources = append(sources, identity)
		}
		return nil
	}
	commands, err := faultDocumentValue(root, "command_mocks")
	if err != nil {
		return nil, err
	}
	if commands != nil && commands.Tag != "!!null" {
		commands, err = faultAliasTarget(commands)
		if err != nil {
			return nil, err
		}
		if commands.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("command_mocks must be a sequence")
		}
		for _, command := range commands.Content {
			name, err := faultDocumentValue(command, "name")
			if err != nil {
				return nil, err
			}
			responses, err := faultDocumentValue(command, "responses")
			if err != nil {
				return nil, err
			}
			identity := MockResponseSource{Kind: "command"}
			if name != nil {
				name, err = faultAliasTarget(name)
				if err != nil {
					return nil, err
				}
				identity.Command = name.Value
			}
			if err := appendResponses(responses, identity); err != nil {
				return nil, err
			}
		}
	}
	servers, err := faultDocumentValue(root, "mcp_mocks")
	if err != nil {
		return nil, err
	}
	if servers != nil && servers.Tag != "!!null" {
		servers, err = faultAliasTarget(servers)
		if err != nil {
			return nil, err
		}
		if servers.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("mcp_mocks must be a sequence")
		}
		for _, server := range servers.Content {
			name, err := faultDocumentValue(server, "name")
			if err != nil {
				return nil, err
			}
			if name != nil {
				name, err = faultAliasTarget(name)
				if err != nil {
					return nil, err
				}
			}
			tools, err := faultDocumentValue(server, "tools")
			if err != nil {
				return nil, err
			}
			if tools == nil {
				continue
			}
			fields, err := faultEffectiveFields(tools, make(map[*yaml.Node]bool))
			if err != nil {
				return nil, err
			}
			for i := 0; i < len(fields); i += 2 {
				responses, err := faultDocumentValue(fields[i+1], "responses")
				if err != nil {
					return nil, err
				}
				identity := MockResponseSource{Kind: "mcp", Tool: fields[i].Value}
				if name != nil {
					identity.Server = name.Value
				}
				if err := appendResponses(responses, identity); err != nil {
					return nil, err
				}
			}
		}
	}
	return sources, nil
}

func faultDocumentValue(node *yaml.Node, key string) (*yaml.Node, error) {
	budget := 100000
	return lookupFaultValue(node, key, make(map[*yaml.Node]bool), &budget)
}

func lookupFaultValue(node *yaml.Node, key string, active map[*yaml.Node]bool, budget *int) (*yaml.Node, error) {
	*budget--
	if *budget < 0 {
		return nil, fmt.Errorf("mock configuration YAML merge traversal exceeds limits")
	}
	if node == nil {
		return nil, nil
	}
	node, err := faultAliasTarget(node)
	if err != nil {
		return nil, err
	}
	if active[node] {
		return nil, fmt.Errorf("mock configuration contains a cyclic YAML merge")
	}
	if len(active) > 1000 {
		return nil, fmt.Errorf("mock configuration YAML merge depth exceeds limits")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("mock configuration must be a mapping")
	}
	var value *yaml.Node
	var merges []*yaml.Node
	for i := 0; i < len(node.Content); i += 2 {
		field, err := faultAliasTarget(node.Content[i])
		if err != nil {
			return nil, err
		}
		if field.Tag == "!!merge" {
			merges = append(merges, node.Content[i+1])
		} else if field.Value == key {
			if value != nil {
				return nil, fmt.Errorf("duplicate mock configuration field %q", key)
			}
			value = node.Content[i+1]
		}
	}
	if value != nil {
		return value, nil
	}
	for _, merged := range merges {
		merged, err := faultAliasTarget(merged)
		if err != nil {
			return nil, err
		}
		mappings := []*yaml.Node{merged}
		if merged.Kind == yaml.SequenceNode {
			mappings = merged.Content
		}
		for _, mapping := range mappings {
			value, err := lookupFaultValue(mapping, key, active, budget)
			if err != nil {
				return nil, err
			}
			if value != nil {
				return value, nil
			}
		}
	}
	return nil, nil
}

// Source admission runs before typed decoding can normalize numeric tags or
// compile matcher resources. Standalone tasks prove shape, not eval eligibility.
func validateDeclaredFaults(data []byte, version string, task bool) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("fault source YAML: %w", err)
	}
	if len(document.Content) != 1 {
		return nil
	}
	root := document.Content[0]
	finite, err := faultPathPresent(root, []string{"command_mocks", "[]", "responses", "[]", "sequence"}, nil)
	if err != nil {
		return err
	}
	if !task {
		mcpFinite, err := faultPathPresent(root, []string{"mcp_mocks", "[]", "tools", "*", "responses", "[]", "sequence"}, nil)
		if err != nil {
			return err
		}
		finite = finite || mcpFinite
	}
	if !finite {
		return nil
	}
	sources, err := MockResponseSources(data)
	if err != nil {
		return err
	}
	if task {
		version = ScenarioSchemaVersion
	}
	for _, source := range sources {
		var err error
		switch source.Kind {
		case "command":
			err = ValidateCommandFaultResponse(source.Data, FaultSourceYAML, version)
		case "mcp":
			if !task {
				err = ValidateMCPFaultResponse(source.Data, FaultSourceYAML, version)
			}
		}
		if err != nil {
			return fmt.Errorf("%s mock response[%d]: %w", source.Kind, source.Index, err)
		}
	}
	return nil
}

type faultPathVisit struct {
	node  *yaml.Node
	depth int
}

func faultPathPresent(node *yaml.Node, path []string, active map[faultPathVisit]bool) (bool, error) {
	if node == nil {
		return false, nil
	}
	if len(path) == 0 {
		return true, nil
	}
	if active == nil {
		active = make(map[faultPathVisit]bool)
	}
	visit := faultPathVisit{node: node, depth: len(path)}
	if visiting, seen := active[visit]; seen {
		if visiting {
			return false, fmt.Errorf("mock configuration contains a cyclic YAML alias or merge")
		}
		return false, nil
	}
	if len(active) > 100000 {
		return false, fmt.Errorf("mock configuration YAML alias traversal exceeds limits")
	}
	active[visit] = true
	defer func() { active[visit] = false }()
	if node.Kind == yaml.AliasNode {
		return faultPathPresent(node.Alias, path, active)
	}
	if path[0] == "[]" {
		if node.Kind == yaml.SequenceNode {
			for _, child := range node.Content {
				found, err := faultPathPresent(child, path[1:], active)
				if err != nil || found {
					return found, err
				}
			}
		}
		return false, nil
	}
	if node.Kind != yaml.MappingNode {
		return false, nil
	}
	if path[0] != "*" {
		value, err := faultDocumentValue(node, path[0])
		if err != nil {
			return false, err
		}
		return faultPathPresent(value, path[1:], active)
	}
	fields, err := faultEffectiveFields(node, make(map[*yaml.Node]bool))
	if err != nil {
		return false, err
	}
	for i := 0; i < len(fields); i += 2 {
		found, err := faultPathPresent(fields[i+1], path[1:], active)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// Only keys and merge containers are traversed; field values remain untouched.
func faultEffectiveFields(node *yaml.Node, active map[*yaml.Node]bool) ([]*yaml.Node, error) {
	budget := 100000
	return walkFaultEffectiveFields(node, active, &budget)
}

func walkFaultEffectiveFields(node *yaml.Node, active map[*yaml.Node]bool, budget *int) ([]*yaml.Node, error) {
	*budget--
	if *budget < 0 {
		return nil, fmt.Errorf("mock configuration YAML merge traversal exceeds limits")
	}
	node, err := faultAliasTarget(node)
	if err != nil {
		return nil, err
	}
	if active[node] || len(active) > 1000 {
		return nil, fmt.Errorf("mock source YAML merge is cyclic or exceeds depth limits")
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("mock configuration must be a mapping")
	}
	active[node] = true
	defer delete(active, node)
	var fields []*yaml.Node
	var merges []*yaml.Node
	seen := make(map[string]bool)
	for i := 0; i < len(node.Content); i += 2 {
		key, err := faultAliasTarget(node.Content[i])
		if err != nil {
			return nil, err
		}
		if key.Tag == "!!merge" {
			merges = append(merges, node.Content[i+1])
			continue
		}
		fields = append(fields, key, node.Content[i+1])
		seen[key.Tag+"\x00"+key.Value] = true
	}
	for _, merged := range merges {
		merged, err := faultAliasTarget(merged)
		if err != nil {
			return nil, err
		}
		mappings := []*yaml.Node{merged}
		if merged.Kind == yaml.SequenceNode {
			mappings = merged.Content
		}
		for _, mapping := range mappings {
			inherited, err := walkFaultEffectiveFields(mapping, active, budget)
			if err != nil {
				return nil, err
			}
			previous := maps.Clone(seen)
			for i := 0; i < len(inherited); i += 2 {
				key := inherited[i].Tag + "\x00" + inherited[i].Value
				if !previous[key] {
					fields = append(fields, inherited[i], inherited[i+1])
					seen[key] = true
				}
			}
		}
	}
	return fields, nil
}

func faultAliasTarget(node *yaml.Node) (*yaml.Node, error) {
	seen := make(map[*yaml.Node]bool)
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil, fmt.Errorf("mock source contains a cyclic YAML alias")
		}
		seen[node], node = true, node.Alias
	}
	if node == nil {
		return nil, fmt.Errorf("mock source contains a missing YAML alias")
	}
	return node, nil
}

func faultNodeHasKey(node *yaml.Node, key string) bool {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				return true
			}
		}
	}
	return false
}

// Detachment expands aliases while their original targets remain available.
// Explicit duplicates survive, but YAML merges use their native precedence.
func detachedFaultNode(node *yaml.Node) (*yaml.Node, error) {
	active := make(map[*yaml.Node]bool)
	budget := 100000
	var clone func(*yaml.Node, int) (*yaml.Node, error)
	clone = func(node *yaml.Node, depth int) (*yaml.Node, error) {
		budget--
		if node == nil || active[node] {
			return nil, fmt.Errorf("mock source contains a cyclic or missing YAML alias")
		}
		if budget < 0 || depth > 1000 {
			return nil, fmt.Errorf("mock source YAML alias expansion exceeds limits")
		}
		active[node] = true
		defer delete(active, node)
		if node.Kind == yaml.AliasNode {
			return clone(node.Alias, depth+1)
		}
		result := *node
		result.Anchor, result.Alias, result.Content = "", nil, nil
		var merges []*yaml.Node
		for i := 0; i < len(node.Content); i++ {
			if node.Kind == yaml.MappingNode && i%2 == 0 && node.Content[i].Tag == "!!merge" {
				merged, err := clone(node.Content[i+1], depth+1)
				if err != nil {
					return nil, err
				}
				merges = append(merges, merged)
				i++
				continue
			}
			child, err := clone(node.Content[i], depth+1)
			if err != nil {
				return nil, err
			}
			result.Content = append(result.Content, child)
		}
		seen := make(map[string]bool)
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(result.Content); i += 2 {
				seen[result.Content[i].Tag+"\x00"+result.Content[i].Value] = true
			}
		}
		var merge func(*yaml.Node) error
		merge = func(merged *yaml.Node) error {
			if merged.Kind == yaml.SequenceNode {
				for _, mapping := range merged.Content {
					if mapping.Kind != yaml.MappingNode {
						return fmt.Errorf("mock source YAML merge must contain mappings")
					}
					if err := merge(mapping); err != nil {
						return err
					}
				}
				return nil
			}
			if merged.Kind != yaml.MappingNode {
				return fmt.Errorf("mock source YAML merge must contain mappings")
			}
			previous := maps.Clone(seen)
			for i := 0; i < len(merged.Content); i += 2 {
				key := merged.Content[i].Tag + "\x00" + merged.Content[i].Value
				if !previous[key] {
					result.Content = append(result.Content, merged.Content[i], merged.Content[i+1])
					seen[key] = true
				}
			}
			return nil
		}
		for _, merged := range merges {
			if err := merge(merged); err != nil {
				return nil, err
			}
		}
		return &result, nil
	}
	return clone(node, 0)
}
