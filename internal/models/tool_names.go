package models

import "strings"

// CanonicalToolName is the exact, case-insensitive tool-name contract shared by
// runtime policy enforcement and the allow_only grader. MCP names include the
// server key (server-tool); a bare tool name never represents an entire server.
func CanonicalToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"builtin:", "mcp:", "custom:"} {
		if strings.HasPrefix(name, prefix) {
			if prefix != "builtin:" {
				return name
			}
			name = strings.TrimPrefix(name, prefix)
			break
		}
	}

	switch name {
	case "readfile", "fileread", "view":
		return "read"
	case "writefile", "filewrite", "edit":
		return "write"
	case "runcommand":
		return "bash"
	case "web_fetch":
		return "fetch"
	default:
		return name
	}
}

// MatchesToolCallName compares a declaration with a hook/transcript name.
// These observations may omit source metadata; in that case source isolation
// relies on the SDK filter, and custom/MCP names match literally without aliases.
// Permission requests retain their source and use CanonicalToolName directly.
func MatchesToolCallName(declared, observed string) bool {
	declared = strings.ToLower(strings.TrimSpace(declared))
	observed = strings.ToLower(strings.TrimSpace(observed))
	if !strings.Contains(observed, ":") {
		for _, prefix := range []string{"mcp:", "custom:"} {
			if strings.HasPrefix(declared, prefix) {
				return strings.TrimPrefix(declared, prefix) == observed
			}
		}
	}
	return CanonicalToolName(declared) == CanonicalToolName(observed)
}
