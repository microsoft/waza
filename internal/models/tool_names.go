package models

import "strings"

// CanonicalToolName is the exact, case-insensitive tool-name contract shared by
// runtime policy enforcement and the allow_only grader. MCP names include the
// server key (server-tool); a bare tool name never represents an entire server.
func CanonicalToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"builtin:", "mcp:", "custom:"} {
		if strings.HasPrefix(name, prefix) {
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
