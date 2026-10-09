package models

import (
	"fmt"
	"regexp"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateMCPMockResponse validates the existing MCP argument matchers.
func ValidateMCPMockResponse(response MCPMockResponse) error {
	return ValidateMCPMockResponseWithLoader(response, nil)
}

// ValidateMCPMockResponseWithLoader preserves the caller's resource policy.
// A nil loader retains the compiler's legacy default, including file loading.
func ValidateMCPMockResponseWithLoader(response MCPMockResponse, loader jsonschema.URLLoader) error {
	for field, pattern := range response.MatchRegex {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("match_regex field %q has invalid regex %q: %w", field, pattern, err)
		}
	}
	if len(response.MatchSchema) > 0 {
		compiler := jsonschema.NewCompiler()
		if loader != nil {
			compiler.UseLoader(loader)
		}
		if err := compiler.AddResource("memory://mcp-mock-schema.json", response.MatchSchema); err != nil {
			return fmt.Errorf("match_schema is invalid: %w", err)
		}
		if _, err := compiler.Compile("memory://mcp-mock-schema.json"); err != nil {
			return fmt.Errorf("match_schema is invalid: %w", err)
		}
	}
	return nil
}
