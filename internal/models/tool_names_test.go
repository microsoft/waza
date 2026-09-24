package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalToolName(t *testing.T) {
	for input, want := range map[string]string{
		" readFile ": "read", "fileRead": "read", "builtin:VIEW": "read",
		"writeFile": "write", "fileWrite": "write", "edit": "write",
		"runCommand": "bash", "web_fetch": "fetch", "fetch": "fetch",
		"mcp:github-list_issues": "mcp:github-list_issues", "custom:CodeSearch": "custom:codesearch",
		"bashful": "bashful", "": "", "custom:view": "custom:view",
	} {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, want, CanonicalToolName(input))
			require.Equal(t, want, CanonicalToolName(CanonicalToolName(input)))
		})
	}
}
func TestMatchesToolCallName(t *testing.T) {
	for _, tc := range []struct {
		declared, observed string
		want               bool
	}{
		{"fileRead", "builtin:view", true},
		{"fileRead", "custom:view", false},
		{"custom:view", "view", true},
		{"custom:view", "read", false},
		{"custom:view", "builtin:view", false},
		{"CodeSearch", "custom:CodeSearch", false},
		{"custom:CodeSearch", "CodeSearch", true},
		{"github-list_issues", "mcp:github-list_issues", false},
		{"mcp:github-list_issues", "github-list_issues", true},
		{"mcp:github-list_issues", "custom:github-list_issues", false},
		{"mcp:github-list_issues", "other-list_issues", false},
	} {
		t.Run(tc.declared+"/"+tc.observed, func(t *testing.T) {
			require.Equal(t, tc.want, MatchesToolCallName(tc.declared, tc.observed))
		})
	}
}
