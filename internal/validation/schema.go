package validation

import (
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
func ValidateEvalFile(evalPath string) (evalErrs []string, taskErrs map[string][]string, err error) {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading eval file: %w", err)
	}

	// Validate eval schema
	evalErrs = ValidateEvalBytes(data)

	// Parse into a minimal struct to resolve task globs
	var spec struct {
		Tasks []string `yaml:"tasks"`
	}
	if yamlErr := yaml.Unmarshal(data, &spec); yamlErr != nil {
		return evalErrs, nil, nil // can't resolve tasks, but eval errors are still useful
	}

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
	if err := yaml.Unmarshal(data, &yamlDoc); err != nil {
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
