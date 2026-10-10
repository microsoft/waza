package models

import (
	"fmt"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/schemaloader"
	"gopkg.in/yaml.v3"
)

// Native matcher decoding compiles schemas eagerly with the legacy loader.
// Examine only schema-bearing declarations, on the same immutable bytes, before
// handing the document to that decoder. Returned payloads are not schemas.
type schemaGuardGrader struct {
	Kind   GraderKind `yaml:"type"`
	Config yaml.Node  `yaml:"config"`
}

// DecodeGraderConfigOffline guards cached/merged grader declarations before
// their native argument matcher decoder can load external schema resources.
func DecodeGraderConfigOffline(data []byte) (GraderConfig, error) {
	var declaration schemaGuardGrader
	if err := yaml.Unmarshal(data, &declaration); err != nil {
		return GraderConfig{}, err
	}
	if err := guardOfflineGraderSchema(declaration); err != nil {
		return GraderConfig{}, err
	}
	var grader GraderConfig
	err := yaml.Unmarshal(data, &grader)
	return grader, err
}

func guardOfflineModelSchemas(data []byte, task bool) error {
	var document struct {
		Graders     []schemaGuardGrader `yaml:"graders"`
		Checkpoints []struct {
			Graders []schemaGuardGrader `yaml:"graders"`
		} `yaml:"checkpoints"`
		MCPMocks []MCPMockConfig `yaml:"mcp_mocks"`
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
	for _, declaration := range declarations {
		if err := guardOfflineGraderSchema(declaration); err != nil {
			return err
		}
	}
	if !task {
		for _, mock := range document.MCPMocks {
			for _, tool := range mock.Tools {
				for _, response := range tool.Responses {
					if err := ValidateMCPMockResponseWithLoader(response, schemaloader.Offline{}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
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
	for _, field := range fields {
		node, ok := config[field]
		if !ok {
			continue
		}
		var expectations []struct {
			Args map[string]map[string]any `yaml:"args"`
		}
		if err := node.Decode(&expectations); err != nil {
			return err
		}
		for _, expectation := range expectations {
			for _, value := range expectation.Args {
				if len(value) != 1 {
					return fmt.Errorf("argument matcher must have exactly one kind")
				}
				if schema, ok := value["json_schema"]; ok {
					doc, ok := schema.(map[string]any)
					if !ok {
						return fmt.Errorf("argument json_schema must be a mapping")
					}
					matcher := argmatcher.Matcher{Kind: argmatcher.KindJSONSchema, JSONSchema: doc}
					if err := matcher.CompileWithLoader(schemaloader.Offline{}); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
