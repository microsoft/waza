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
		"mcp:github-list_issues": "github-list_issues", "custom:CodeSearch": "codesearch",
		"bashful": "bashful", "": "", "custom:view": "read",
	} {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, want, CanonicalToolName(input))
			require.Equal(t, want, CanonicalToolName(CanonicalToolName(input)))
		})
	}
}
