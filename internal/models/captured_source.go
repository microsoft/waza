package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// These stricter captured-byte entrypoints do not alter same-major compatibility
// warnings in the ordinary path loaders.
func validateCapturedModelSource(data []byte, task bool) error {
	if err := guardOfflineModelSchemas(data, task); err != nil {
		return err
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("captured source requires one nonnull mapping document")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("captured source must contain exactly one YAML document")
	}
	target := reflect.TypeFor[EvalSpec]()
	if task {
		target = reflect.TypeFor[TestCase]()
	}
	if err := checkCapturedFields(&doc, target); err != nil {
		return err
	}
	for _, g := range mappingValue(doc.Content[0], "graders").Content {
		kind := mappingValue(g, "type")
		if kind.Value == string(GraderKindText) {
			if err := checkCapturedFields(mappingValue(g, "config"), reflect.TypeFor[TextGraderParameters]()); err != nil {
				return err
			}
		}
	}
	if task {
		prompt := mappingValue(mappingValue(doc.Content[0], "inputs"), "prompt_file")
		if prompt.Kind != 0 && prompt.Value != "" &&
			(!filepath.IsLocal(prompt.Value) || filepath.Clean(prompt.Value) != prompt.Value || strings.Contains(prompt.Value, "\\")) {
			return fmt.Errorf("unsupported noncanonical captured prompt_file path")
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, name string) *yaml.Node {
	if node.Kind == yaml.MappingNode {
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				return node.Content[i+1]
			}
		}
	}
	return &yaml.Node{}
}

func checkCapturedFields(node *yaml.Node, target reflect.Type) error {
	if node.Kind == 0 {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("unsupported captured YAML alias/anchor")
	}
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) != 1 {
			return fmt.Errorf("captured source requires a document")
		}
		return checkCapturedFields(node.Content[0], target)
	}
	nullable := target.Kind() == reflect.Pointer || target.Kind() == reflect.Map ||
		target.Kind() == reflect.Slice || target.Kind() == reflect.Interface
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		if nullable {
			return nil
		}
		return fmt.Errorf("captured native %s field must not be null", target)
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if node.Tag == "!!float" || target.Kind() == reflect.Float32 || target.Kind() == reflect.Float64 {
		return fmt.Errorf("unsupported authored floating-point declaration identity")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if target.Kind() != reflect.Struct && target.Kind() != reflect.Map && target.Kind() != reflect.Interface {
			return fmt.Errorf("captured native %s field does not accept a mapping", target)
		}
		fields := map[string]reflect.Type{}
		var collect func(reflect.Type)
		collect = func(t reflect.Type) {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := strings.Split(f.Tag.Get("yaml"), ",")
				if len(tag) > 1 && tag[1] == "inline" {
					collect(f.Type)
				} else {
					name := tag[0]
					if name == "" {
						name = strings.ToLower(f.Name)
					}
					fields[name] = f.Type
				}
			}
		}
		if target.Kind() == reflect.Struct {
			collect(target)
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || seen[key.Value] || key.Value == "<<" {
				return fmt.Errorf("captured source requires unique string mapping keys")
			}
			seen[key.Value] = true
			child := reflect.TypeFor[any]()
			if target.Kind() == reflect.Struct {
				var ok bool
				child, ok = fields[key.Value]
				if !ok {
					return fmt.Errorf("unknown captured native field %q", key.Value)
				}
			} else if target.Kind() == reflect.Map {
				child = target.Elem()
			}
			if err := checkCapturedFields(value, child); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		if target.Kind() != reflect.Slice && target.Kind() != reflect.Array && target.Kind() != reflect.Interface {
			return fmt.Errorf("captured native %s field does not accept a sequence", target)
		}
		child := reflect.TypeFor[any]()
		if target.Kind() == reflect.Slice || target.Kind() == reflect.Array {
			child = target.Elem()
		}
		for _, value := range node.Content {
			if err := checkCapturedFields(value, child); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		switch target.Kind() {
		case reflect.String:
			if node.Tag != "!!str" {
				return fmt.Errorf("captured native string field requires !!str, got %s", node.Tag)
			}
		case reflect.Bool:
			if node.Tag != "!!bool" {
				return fmt.Errorf("captured native bool field requires !!bool, got %s", node.Tag)
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			if node.Tag != "!!int" {
				return fmt.Errorf("captured native integer field requires !!int, got %s", node.Tag)
			}
		case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
			return fmt.Errorf("captured native %s field does not accept a scalar", target)
		}
	default:
		return fmt.Errorf("unsupported captured native YAML node kind")
	}
	return nil
}

func capturedTaskMetadata(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	// Use the actual native decoder's integer/radix/timestamp semantics, not a
	// separate YAML-to-JSON interpretation. Reject integers it rounded to float.
	var check func(any) error
	check = func(value any) error {
		switch value := value.(type) {
		case float32, float64:
			return fmt.Errorf("unsupported rounded native context number")
		case map[string]any:
			for _, child := range value {
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(metadata); err != nil {
		return nil, err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result map[string]any
	err = decoder.Decode(&result)
	return result, err
}
