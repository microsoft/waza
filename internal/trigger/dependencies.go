package trigger

// UsesEvalMCP reports whether the discovered phase constructs a Runner, which
// resolves eval-level MCP dependencies even before scheduling its prompts.
// The mock executor synthesizes trigger results without constructing a Runner.
func UsesEvalMCP(spec *TestSpec, engineType string) bool {
	return spec != nil && engineType != "mock"
}
