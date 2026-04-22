// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

// cspell: ignore santhosh tekuri

package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// defaultPrinter is used to format schema validation error messages.
var defaultPrinter = message.NewPrinter(language.English)

// evalSchema is the compiled JSON Schema for eval.yaml files.
var evalSchema *jsonschema.Schema

// taskSchema is the compiled JSON Schema for task YAML files.
var taskSchema *jsonschema.Schema

func init() {
	evalSchema = mustCompileSchema(schemas.EvalSchemaJSON, "eval.schema.json")
	taskSchema = mustCompileSchema(schemas.TaskSchemaJSON, "task.schema.json")
}

func mustCompileSchema(raw string, name string) *jsonschema.Schema {
	var schemaDoc any
	if err := json.Unmarshal([]byte(raw), &schemaDoc); err != nil {
		panic(fmt.Sprintf("failed to parse embedded %s: %v", name, err))
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name, schemaDoc); err != nil {
		panic(fmt.Sprintf("failed to add %s resource: %v", name, err))
	}

	sch, err := compiler.Compile(name)
	if err != nil {
		panic(fmt.Sprintf("failed to compile %s: %v", name, err))
	}
	return sch
}

// ValidateEvalFile validates an eval.yaml file at the given path against the JSON schema.
// Returns errors for the eval itself AND all referenced task files.
//
// This assumes that the schema files are up-to-date with the implementation.
// Other YAML decoding errors may be reported if fields are removed from the implementation. Those
// will be caught by the strict YAML parsing in LoadTestCase and LoadEvalSpec, but the schema
// validation has a much higher fidelity (better error location) than the validation in LoadTestCase
// and LoadEvalSpec.
func ValidateEvalFile(evalPath string) (evalErrs []string, taskErrs map[string][]string, err error) {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading eval file: %w", err)
	}

	// Validate the data against the eval schema
	evalErrs = ValidateEvalBytes(data)

	// Since we're just validating the eval.yaml and its referenced tasks, we don't need to parse the
	// full spec here.
	// But a spec must have at least one task reference, so we'll parse out the "tasks" field to find the referenced task files.
	//
	// Note that we're NOT performing a full validation here.
	// If the "tasks" field is missing or not an array of strings, we'll return
	// any schema validation errors in addition to the YAML parsing error for
	// the "tasks" field, but we won't attempt to validate any tasks.
	var spec struct {
		Tasks []string `yaml:"tasks"`
	}
	if yamlErr := yaml.Unmarshal(data, &spec); yamlErr != nil {
		evalErrs = append(evalErrs, fmt.Sprintf("yaml 'tasks' parse: %v", yamlErr))
		return evalErrs, nil, nil
	}

	// Now walk the set of tasks referenced by the eval.yaml and validate each one.
	baseDir := filepath.Dir(evalPath)
	taskErrs = make(map[string][]string)

	for _, pattern := range spec.Tasks {
		fullPattern := filepath.Join(baseDir, pattern)
		matches, globErr := filepath.Glob(fullPattern)
		if globErr != nil {
			continue
		}
		for _, taskFile := range matches {
			taskData, readErr := os.ReadFile(taskFile)
			if readErr != nil {
				continue
			}
			errs := ValidateTaskBytes(taskData)
			if len(errs) > 0 {
				relPath, relErr := filepath.Rel(baseDir, taskFile)
				if relErr != nil {
					relPath = taskFile
				}
				taskErrs[relPath] = errs
			}
		}
	}

	return evalErrs, taskErrs, nil
}

// ValidateEvalBytes validates raw YAML bytes against the eval schema.
func ValidateEvalBytes(data []byte) []string {
	return validateYAMLBytes(evalSchema, data)
}

// ValidateTaskBytes validates raw YAML bytes against the task schema.
func ValidateTaskBytes(data []byte) []string {
	return validateYAMLBytes(taskSchema, data)
}

func validateYAMLBytes(schema *jsonschema.Schema, data []byte) []string {
	// Parse YAML into generic any
	var yamlDoc any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&yamlDoc); err != nil {
		return []string{fmt.Sprintf("YAML parse error: %v", err)}
	}

	// Convert to JSON-compatible types (yaml.v3 uses map[string]any which is fine)
	jsonCompatible := convertToJSONCompatible(yamlDoc)

	return validateAgainstSchema(schema, jsonCompatible)
}

func validateAgainstSchema(schema *jsonschema.Schema, instance any) []string {
	err := schema.Validate(instance)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{fmt.Sprintf("schema: %v", err)}
	}
	var errs []string
	collectSchemaErrors(ve, &errs)
	return errs
}

func collectSchemaErrors(ve *jsonschema.ValidationError, errs *[]string) {
	if len(ve.Causes) == 0 {
		loc := "/"
		if len(ve.InstanceLocation) > 0 {
			loc = "/" + strings.Join(ve.InstanceLocation, "/")
		}
		*errs = append(*errs, fmt.Sprintf("%s: %s", loc, ve.ErrorKind.LocalizedString(defaultPrinter)))
		return
	}
	for _, c := range ve.Causes {
		collectSchemaErrors(c, errs)
	}
}

// convertToJSONCompatible converts YAML-decoded values to JSON-compatible types.
// yaml.v3 decodes to map[string]any which is fine, but integers need to stay as-is.
func convertToJSONCompatible(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any, len(val))
		for k, v2 := range val {
			result[k] = convertToJSONCompatible(v2)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, v2 := range val {
			result[i] = convertToJSONCompatible(v2)
		}
		return result
	default:
		return val
	}
}
