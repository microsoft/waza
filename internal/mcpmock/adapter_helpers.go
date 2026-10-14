package mcpmock

import (
	"fmt"

	"github.com/microsoft/waza/internal/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// MatchesWithLoader exposes existing matching semantics with explicit resource
// policy and configuration errors rather than a live-tool fallback.
func (r Response) MatchesWithLoader(args map[string]any, loader jsonschema.URLLoader) (bool, error) {
	if len(r.Match) > 0 && !exactMatch(r.Match, args) {
		return false, nil
	}
	if len(r.MatchRegex) > 0 {
		if err := models.ValidateMCPMockResponse(models.MCPMockResponse{MatchRegex: r.MatchRegex}); err != nil {
			return false, err
		}
		if !regexMatch(r.MatchRegex, args) {
			return false, nil
		}
	}
	if len(r.MatchSchema) > 0 {
		return schemaMatchWithLoader(r.MatchSchema, args, loader)
	}
	return true, nil
}

func schemaMatchWithLoader(schemaDoc map[string]any, args map[string]any, loader jsonschema.URLLoader) (bool, error) {
	compiler := jsonschema.NewCompiler()
	if loader != nil {
		compiler.UseLoader(loader)
	}
	if err := compiler.AddResource("memory://mcp-mock-schema.json", schemaDoc); err != nil {
		return false, fmt.Errorf("adding MCP matcher schema: %w", err)
	}
	schema, err := compiler.Compile("memory://mcp-mock-schema.json")
	if err != nil {
		return false, fmt.Errorf("compiling MCP matcher schema: %w", err)
	}
	// A validation mismatch is not an operational failure. Do not expose values.
	return schema.Validate(args) == nil, nil
}
