// Package faultfixture validates fault response source before normalization.
// It does not enable public sequence execution or provide runtime evidence.
package faultfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type Format string

const (
	JSON Format = "json"
	YAML Format = "yaml"
)

var commandFields = []string{"args", "args_regex", "environment", "workdir", "stdout", "stderr", "exit_code", "fixture"}
var commandOutputs = []string{"stdout", "stderr", "exit_code", "fixture"}
var mcpFields = []string{"match", "match_schema", "match_regex", "return", "error"}
var mcpOutputs = []string{"return", "error"}

// ValidateCommandResponse checks a CLI matcher and any finite source sequence.
// Sequence eligibility requires the explicit enclosing scenario version.
func ValidateCommandResponse(data []byte, format Format, enclosingVersion string) error {
	return validateResponse(data, format, enclosingVersion, commandFields, commandOutputs, validateCommand)
}

// ValidateMCPResponse checks an MCP matcher and any finite source sequence.
// Callers must run this before serialization can erase zero/null field presence.
func ValidateMCPResponse(data []byte, format Format, enclosingVersion string) error {
	loader := schemaloader.Offline{}
	return validateResponse(data, format, enclosingVersion, mcpFields, mcpOutputs, func(source object, step bool) error {
		return validateMCP(source, step, loader)
	})
}

type object map[string]json.RawMessage

func validateResponse(data []byte, format Format, version string, fields, outputs []string, validate func(object, bool) error) error {
	source, duplicates, err := decodeSource(data, format)
	if err != nil {
		return err
	}
	rawSequence, hasSequence := source["sequence"]
	if _, hasDelay := source["delay_ms"]; hasDelay && !hasSequence {
		return fmt.Errorf("delay_ms is only supported inside a finite sequence step")
	}
	if hasSequence && version != models.ScenarioSchemaVersion {
		return fmt.Errorf("fault sequence requires explicit enclosing scenario schemaVersion %s", models.ScenarioSchemaVersion)
	}
	if hasSequence && len(duplicates) > 0 {
		return fmt.Errorf("fault response has duplicate field %q", duplicates[0])
	}
	for _, field := range slices.Sorted(maps.Keys(source)) {
		if field == "sequence" || slices.Contains(fields, field) {
			continue
		}
		if hasSequence || version == models.ScenarioSchemaVersion {
			return fmt.Errorf("unknown fault response field %q", field)
		}
		slog.Warn("unknown schema field ignored for same-major compatibility", "artifact", "mock response", "detail", fmt.Sprintf("field %q", field))
	}
	if !hasSequence {
		return validate(source, false)
	}
	for _, field := range outputs {
		if _, present := source[field]; present {
			return fmt.Errorf("fault response cannot mix sequence with field %q, even when empty, zero or null", field)
		}
	}
	matcher := maps.Clone(source)
	delete(matcher, "sequence")
	if err := validate(matcher, false); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(rawSequence), []byte("null")) {
		return fmt.Errorf("fault sequence must be a non-empty array, not null")
	}
	var steps []json.RawMessage
	if err := json.Unmarshal(rawSequence, &steps); err != nil {
		return fmt.Errorf("decoding fault sequence array: %w", err)
	}
	if len(steps) == 0 {
		return fmt.Errorf("fault sequence must contain at least one step")
	}
	for index, rawStep := range steps {
		step, duplicates, err := decodeObject(rawStep)
		if err != nil {
			return fmt.Errorf("fault sequence[%d]: %w", index, err)
		}
		if len(duplicates) > 0 {
			return fmt.Errorf("fault sequence[%d] has duplicate field %q", index, duplicates[0])
		}
		hasOutput := false
		for _, field := range slices.Sorted(maps.Keys(step)) {
			if slices.Contains(outputs, field) {
				hasOutput = true
				continue
			}
			if field != "delay_ms" {
				return fmt.Errorf("fault sequence[%d] has unknown or nested matcher/sequence field %q", index, field)
			}
			var delay *int64
			if err := json.Unmarshal(step[field], &delay); err != nil {
				return fmt.Errorf("fault sequence[%d].delay_ms must be an integer: %w", index, err)
			}
			if delay == nil {
				return fmt.Errorf("fault sequence[%d].delay_ms must be an integer, not null", index)
			}
			if err := faultsequence.ValidateDelayMilliseconds(*delay); err != nil {
				return fmt.Errorf("fault sequence[%d]: %w", index, err)
			}
		}
		if !hasOutput {
			return fmt.Errorf("fault sequence[%d] must explicitly define an output or error field", index)
		}
		if err := validate(step, true); err != nil {
			return fmt.Errorf("fault sequence[%d]: %w", index, err)
		}
	}
	return nil
}

func validateCommand(source object, step bool) error {
	fields := maps.Clone(source)
	delete(fields, "delay_ms")
	if step {
		_, stdout := fields["stdout"]
		_, fixture := fields["fixture"]
		if stdout && fixture {
			return fmt.Errorf("cannot specify both fixture and stdout")
		}
		fields["args"] = json.RawMessage(`[]`)
		if raw, present := fields["exit_code"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("exit_code must be an integer, not null")
		}
		for _, field := range []string{"stderr", "fixture"} {
			if raw, present := fields[field]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("%s must be a string, not null", field)
			}
		}
	}
	var response models.CommandMockResponse
	if err := decodeTyped(fields, &response); err != nil {
		return fmt.Errorf("decoding command mock response: %w", err)
	}
	return models.ValidateCommandMocks([]models.CommandMockConfig{{Name: "fixture", Responses: []models.CommandMockResponse{response}}})
}

func validateMCP(source object, step bool, loader jsonschema.URLLoader) error {
	if step {
		_, returns := source["return"]
		_, fails := source["error"]
		if returns == fails {
			return fmt.Errorf("MCP step must specify exactly one of return or error")
		}
		if fails {
			var message string
			if err := json.Unmarshal(source["error"], &message); err != nil {
				return fmt.Errorf("decoding MCP fixture error: %w", err)
			}
			if message == "" {
				return fmt.Errorf("MCP fixture error must be a non-empty string")
			}
		}
	}
	var response models.MCPMockResponse
	if err := decodeTyped(source, &response); err != nil {
		return fmt.Errorf("decoding MCP mock response: %w", err)
	}
	return models.ValidateMCPMockResponseWithLoader(response, loader)
}

func decodeTyped(fields object, target any) error {
	data, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encoding mock fields: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func decodeSource(data []byte, format Format) (object, []string, error) {
	switch format {
	case JSON:
		return decodeObject(data)
	case YAML:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		var fields map[string]any
		if err := decoder.Decode(&fields); err != nil {
			return nil, nil, fmt.Errorf("decoding mock response YAML: %w", err)
		}
		if fields == nil {
			return nil, nil, fmt.Errorf("mock response must be an object, not null")
		}
		if err := validateYAMLIntegerFields(fields); err != nil {
			return nil, nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err != nil {
				return nil, nil, fmt.Errorf("decoding trailing mock response YAML: %w", err)
			}
			return nil, nil, fmt.Errorf("mock response must contain exactly one YAML document")
		}
		// YAML decoding resolves merge aliases and rejects duplicate mapping keys.
		canonical, err := json.Marshal(fields)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding mock response YAML fields: %w", err)
		}
		return decodeObject(canonical)
	default:
		return nil, nil, fmt.Errorf("unsupported mock response format %q; use json or yaml", format)
	}
}

func validateYAMLIntegerFields(fields map[string]any) error {
	steps, ok := fields["sequence"].([]any)
	if !ok {
		return nil
	}
	for index, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"delay_ms", "exit_code"} {
			// Do not canonicalize YAML floats into integral JSON numbers.
			if _, isFloat := step[field].(float64); isFloat {
				return fmt.Errorf("fault sequence[%d].%s must be a YAML integer, not a float", index, field)
			}
		}
	}
	return nil
}

func decodeObject(data []byte) (object, []string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("decoding mock response object: %w", err)
	}
	if token != json.Delim('{') {
		return nil, nil, fmt.Errorf("mock response must be a JSON object")
	}
	fields := make(object)
	var duplicates []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("decoding mock response field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, nil, fmt.Errorf("mock response field name must be a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, fmt.Errorf("decoding mock response field %q: %w", key, err)
		}
		if _, exists := fields[key]; exists {
			duplicates = append(duplicates, key)
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, fmt.Errorf("closing mock response object: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, nil, fmt.Errorf("decoding trailing mock response JSON: %w", err)
		}
		return nil, nil, fmt.Errorf("mock response must contain exactly one JSON value")
	}
	return fields, duplicates, nil
}
