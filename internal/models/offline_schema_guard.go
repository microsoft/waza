package models

import (
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Native matcher decoding compiles schemas eagerly with the legacy loader.
// Examine matcher declarations on the same immutable bytes before handing the
// document to that decoder. Returned payloads and literal operands are not schemas.
type schemaGuardGrader struct {
	Kind       GraderKind `yaml:"type"`
	Identifier string     `yaml:"name"`
	Config     yaml.Node  `yaml:"config"`
}

// DecodeGraderConfigOffline guards cached/merged grader declarations before
// their native argument matcher decoder can load external schema resources.
// Validators inspect retained tool parameters when external schemas block decode.
func DecodeGraderConfigOffline(data []byte, validators ...func(string, GraderParameters) error) (GraderConfig, error) {
	var declaration schemaGuardGrader
	if err := yaml.Unmarshal(data, &declaration); err != nil {
		return GraderConfig{}, err
	}
	if err := guardOfflineGraderSchema(declaration); err != nil {
		_, external := schemaloader.ClassifyError(err)
		if external && len(validators) > 0 {
			var node yaml.Node
			if decodeErr := yaml.Unmarshal(data, &node); decodeErr != nil {
				return GraderConfig{}, errors.Join(err, decodeErr)
			}
			errs := []error{err}
			for _, validate := range validators {
				if validate != nil {
					errs = append(errs, validateOfflineDeclaredGrader(&node, false, validate))
				}
			}
			return GraderConfig{}, errors.Join(errs...)
		}
		return GraderConfig{}, err
	}
	var grader GraderConfig
	err := yaml.Unmarshal(data, &grader)
	return grader, err
}

func guardOfflineModelSchemas(data []byte, task bool, validate func(string, GraderParameters) error) error {
	var document struct {
		Graders     []yaml.Node `yaml:"graders"`
		Checkpoints []struct {
			Graders []yaml.Node `yaml:"graders"`
		} `yaml:"checkpoints"`
		MCPMocks []struct {
			Name  string `yaml:"name"`
			Tools map[string]struct {
				InputSchema any               `yaml:"input_schema"`
				Responses   []MCPMockResponse `yaml:"responses"`
			} `yaml:"tools"`
		} `yaml:"mcp_mocks"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("reading schema declarations: %w", err)
	}
	declarations := document.Graders
	if task {
		for _, checkpoint := range document.Checkpoints {
			declarations = append(declarations, checkpoint.Graders...)
		}
	}
	var errs []error
	var validationDeclarations []yaml.Node
	for _, node := range declarations {
		var declaration schemaGuardGrader
		if err := node.Decode(&declaration); err != nil {
			errs = append(errs, err)
			continue
		}
		validationDeclarations = append(validationDeclarations, node)
		if err := guardOfflineGraderSchema(declaration); err != nil {
			errs = append(errs, fmt.Errorf("grader %q: %w", declaration.Identifier, err))
		}
	}
	if task {
		// Report duplicate or missing names even when an external matcher schema
		// blocks the full decode that would otherwise run TestCase.Validate.
		names := make([]MCPMockConfig, len(document.MCPMocks))
		for i, mock := range document.MCPMocks {
			names[i] = MCPMockConfig{Name: mock.Name}
		}
		if err := ValidateMCPMockNames(names); err != nil {
			errs = append(errs, err)
		}
	}
	for _, mock := range document.MCPMocks {
		for _, tool := range mock.Tools {
			for _, response := range tool.Responses {
				if err := ValidateMCPMockResponseWithLoader(response, schemaloader.Offline{}); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	_, external := schemaloader.ClassifyError(errors.Join(errs...))
	if external {
		for _, mock := range document.MCPMocks {
			for name, tool := range mock.Tools {
				if err := ValidateMCPInputSchemaOffline(tool.InputSchema, "mock-input-schema.json"); err != nil {
					errs = append(errs, fmt.Errorf("mcp mock %q tool %q input schema: %w", mock.Name, name, err))
				}
			}
		}
	}
	if external && validate != nil {
		// Tool declarations retain blocked matchers for offline field validation;
		// only other safe declarations may enter the eager native decoder.
		for _, node := range validationDeclarations {
			if err := validateOfflineDeclaredGrader(&node, task, validate); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateMCPInputSchemaOffline validates a supplied tool schema without loading
// external resources. A nil schema retains the runtime's default tool schema.
func ValidateMCPInputSchemaOffline(schema any, location string) error {
	if schema == nil {
		return nil
	}
	doc, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("mock tool input schema must be a JSON object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	if err := compiler.AddResource(location, doc); err != nil {
		return fmt.Errorf("adding mock input schema: %w", err)
	}
	if _, err := compiler.Compile(location); err != nil {
		return fmt.Errorf("compiling mock input schema: %w", err)
	}
	return nil
}

func validateOfflineDeclaredGrader(node *yaml.Node, task bool, validate func(string, GraderParameters) error) error {
	var declaration schemaGuardGrader
	if err := node.Decode(&declaration); err != nil {
		return err
	}
	if declaration.Kind == GraderKindToolCalls || declaration.Kind == GraderKindToolConstraint {
		params, err := decodeOfflineToolParameters(declaration)
		if params == nil {
			return err
		}
		return errors.Join(err, validate(declaration.Identifier, params))
	}
	if task {
		var grader ValidatorInline
		if err := node.Decode(&grader); err != nil {
			return err
		}
		return validate(grader.Identifier, grader.Parameters)
	}
	var grader GraderConfig
	if err := node.Decode(&grader); err != nil {
		return err
	}
	if grader.Ref != "" && grader.Kind == "" {
		return nil
	}
	return validate(grader.Identifier, grader.Parameters)
}

func guardOfflineGraderSchema(declaration schemaGuardGrader) error {
	var fields []string
	switch declaration.Kind {
	case GraderKindToolCalls:
		fields = []string{"expect"}
	case GraderKindToolConstraint:
		fields = []string{"expect_tools", "reject_tools", "allow_only"}
	default:
		return nil
	}
	var config map[string]yaml.Node
	if err := declaration.Config.Decode(&config); err != nil {
		return err
	}
	var errs []error
	for _, field := range fields {
		node, ok := config[field]
		if !ok {
			continue
		}
		var expectations []yaml.Node
		if err := node.Decode(&expectations); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, expectationNode := range expectations {
			var expectation struct {
				Args map[string]yaml.Node `yaml:"args"`
			}
			if err := expectationNode.Decode(&expectation); err != nil {
				errs = append(errs, err)
				continue
			}
			for name, value := range expectation.Args {
				var matcher argmatcher.Matcher
				if err := matcher.DecodeYAMLWithLoader(&value, schemaloader.Offline{}); err != nil {
					errs = append(errs, fmt.Errorf("%s argument %q: %w", field, name, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}
