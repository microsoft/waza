package suggest

import (
	"fmt"
	"io/fs"
	"strings"
)

// graderSummary maps grader type to a one-line description for the selection prompt.
var graderSummary = map[string]string{
	"code":             "Assertion-based: evaluate Python/JS expressions against execution context (output, tool_calls, etc.)",
	"prompt":           "LLM-as-judge: use a language model to evaluate quality via a rubric prompt",
	"text":             "Text matching: check output with substring contains/not_contains and regex_match/regex_not_match",
	"file":             "File validation: verify file existence, absence, and content patterns in workspace",
	"json_schema":      "JSON schema: validate that output is valid JSON conforming to a schema",
	"program":          "External program: run a script/binary that grades via exit code (0=pass)",
	"behavior":         "Behavior constraints: validate tool call counts, token usage, required/forbidden tools",
	"action_sequence":  "Action sequence: validate tool call sequence matches expected pattern (exact/in_order/any_order)",
	"skill_invocation": "Skill invocation: verify dependent skills were invoked in correct sequence",
	"tool_constraint":  "Tool constraints: validate tool usage patterns, turn/token limits",
	"diff":             "File diff: compare workspace files against expected snapshots or line fragments",
}

// GraderSummaries returns a formatted block of one-line grader descriptions
// suitable for the selection prompt (pass 1).
func GraderSummaries() string {
	types := AvailableGraderTypes()
	var b strings.Builder
	for _, t := range types {
		desc := graderSummary[t]
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", t, desc)
	}
	return b.String()
}

// LoadGraderDocs loads the full documentation for the specified grader types
// from the embedded filesystem. Unknown types are silently skipped.
func LoadGraderDocs(fsys fs.FS, types []string) string {
	if fsys == nil {
		return ""
	}
	var b strings.Builder
	for _, t := range types {
		path := fmt.Sprintf("docs/graders/%s.md", t)
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			continue
		}
		b.WriteString(strings.TrimSpace(string(data)))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
