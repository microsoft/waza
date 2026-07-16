package graders

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// jsonSchemaGrader validates that the agent output is valid JSON matching a given schema.
type jsonSchemaGrader struct {
	name        string
	schema      map[string]any
	schemaFile  string
	extractJSON bool
}

// NewJSONSchemaGrader creates a [jsonSchemaGrader] that validates agent output against
// a JSON schema provided inline or via a file path.
func NewJSONSchemaGrader(name string, args models.JSONSchemaGraderParameters) (*jsonSchemaGrader, error) {
	if args.Schema == nil && args.SchemaFile == "" {
		return nil, fmt.Errorf("json_schema grader '%s' must have either 'schema' or 'schema_file'", name)
	}

	return &jsonSchemaGrader{
		name:        name,
		schema:      args.Schema,
		schemaFile:  args.SchemaFile,
		extractJSON: args.ExtractJSON,
	}, nil
}

func (jsg *jsonSchemaGrader) Name() string            { return jsg.name }
func (jsg *jsonSchemaGrader) Kind() models.GraderKind { return models.GraderKindJSONSchema }

func (jsg *jsonSchemaGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		// Step 1: parse the output. Extraction is opt-in so strict JSON output
		// remains the default contract for this grader.
		outputValue, err := parseJSONOutput(gradingContext.Output, jsg.extractJSON)
		if err != nil {
			return &models.GraderResults{
				Name:     jsg.name,
				Type:     models.GraderKindJSONSchema,
				Score:    0.0,
				Passed:   false,
				Feedback: fmt.Sprintf("Output is not valid JSON: %v", err),
				Details: map[string]any{
					"error": err.Error(),
				},
			}, nil
		}

		// Step 2: resolve the schema
		schemaMap, err := jsg.resolveSchema()
		if err != nil {
			return nil, fmt.Errorf("json_schema grader '%s': %w", jsg.name, err)
		}

		// Step 3: validate against schema
		failures, err := validateAgainstSchema(outputValue, schemaMap)
		if err != nil {
			return nil, fmt.Errorf("json_schema grader '%s': %w", jsg.name, err)
		}

		if len(failures) > 0 {
			return &models.GraderResults{
				Name:     jsg.name,
				Type:     models.GraderKindJSONSchema,
				Score:    0.0,
				Passed:   false,
				Feedback: strings.Join(failures, "; "),
				Details: map[string]any{
					"failures": failures,
				},
			}, nil
		}

		return &models.GraderResults{
			Name:     jsg.name,
			Type:     models.GraderKindJSONSchema,
			Score:    1.0,
			Passed:   true,
			Feedback: "Output matches JSON schema",
		}, nil
	})
}

func parseJSONOutput(output string, extractJSON bool) (any, error) {
	var outputValue any
	if err := json.Unmarshal([]byte(output), &outputValue); err == nil {
		return outputValue, nil
	} else if !extractJSON {
		return nil, err
	}

	if values := parseJSONValues(jsonFencedBlocks(output)); len(values) > 0 {
		return exactlyOneJSONValue(values)
	}

	return exactlyOneJSONValue(embeddedJSONValues(output))
}

func jsonFencedBlocks(output string) []string {
	var blocks []string
	for offset := 0; offset < len(output); {
		fenceStart := strings.Index(output[offset:], "```")
		if fenceStart < 0 {
			break
		}
		fenceStart += offset

		headerEnd := strings.IndexByte(output[fenceStart:], '\n')
		if headerEnd < 0 {
			break
		}
		headerEnd += fenceStart
		language := strings.TrimSpace(output[fenceStart+3 : headerEnd])
		contentStart := headerEnd + 1
		fenceEnd := strings.Index(output[contentStart:], "```")
		if fenceEnd < 0 {
			break
		}
		fenceEnd += contentStart

		if language == "" || strings.EqualFold(language, "json") {
			blocks = append(blocks, output[contentStart:fenceEnd])
		}
		offset = fenceEnd + 3
	}
	return blocks
}

func parseJSONValues(documents []string) []any {
	values := make([]any, 0, len(documents))
	for _, document := range documents {
		var value any
		if err := json.Unmarshal([]byte(document), &value); err == nil {
			values = append(values, value)
		}
	}
	return values
}

func embeddedJSONValues(output string) []any {
	var values []any
	for offset := 0; offset < len(output); {
		if output[offset] != '{' && output[offset] != '[' {
			offset++
			continue
		}

		decoder := json.NewDecoder(strings.NewReader(output[offset:]))
		var value any
		if err := decoder.Decode(&value); err != nil {
			offset++
			continue
		}
		values = append(values, value)
		offset += int(decoder.InputOffset())
	}
	return values
}

func exactlyOneJSONValue(values []any) (any, error) {
	switch len(values) {
	case 0:
		return nil, fmt.Errorf("output does not contain a JSON document")
	case 1:
		return values[0], nil
	default:
		return nil, fmt.Errorf("output contains multiple JSON documents")
	}
}

// resolveSchema returns the schema map, loading from file if necessary.
func (jsg *jsonSchemaGrader) resolveSchema() (map[string]any, error) {
	if jsg.schema != nil {
		return jsg.schema, nil
	}

	data, err := os.ReadFile(jsg.schemaFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read schema file %q: %w", jsg.schemaFile, err)
	}

	var schemaMap map[string]any
	if err := json.Unmarshal(data, &schemaMap); err != nil {
		return nil, fmt.Errorf("failed to parse schema file %q: %w", jsg.schemaFile, err)
	}

	return schemaMap, nil
}

// validateAgainstSchema validates the given value against a JSON schema map.
func validateAgainstSchema(value any, schemaMap map[string]any) ([]string, error) {
	schemaJSON, err := json.Marshal(schemaMap)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize schema: %w", err)
	}

	var schemaValue any
	if err := json.Unmarshal(schemaJSON, &schemaValue); err != nil {
		return nil, fmt.Errorf("failed to parse schema for validation: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaValue); err != nil {
		return nil, fmt.Errorf("failed to add schema resource: %w", err)
	}

	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("failed to compile JSON schema: %w", err)
	}

	if err := schema.Validate(value); err != nil {
		var failures []string
		failures = append(failures, fmt.Sprintf("Schema validation failed: %v", err))
		return failures, nil
	}

	return nil, nil
}
