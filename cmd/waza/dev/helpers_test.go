package dev

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/internal/tokens"
	"github.com/stretchr/testify/require"
)

// requireOutputMatch verifies that actual matches expected after masking
// token-count numbers. It then separately verifies that the first displayed
// token count equals wantInitialTokens (the BPE count for the initial skill).
func requireOutputMatch(t *testing.T, expected, actual string, wantInitialTokens int) {
	t.Helper()
	require.Equal(t, testutil.StripTokenCounts(expected), testutil.StripTokenCounts(actual),
		"output format mismatch (token counts masked)")
	got := -1
	var err error
	if m := regexp.MustCompile(`Tokens: (\d+)`).FindStringSubmatch(actual); m != nil {
		got, err = strconv.Atoi(m[1])
		require.NoError(t, err, "parsing token count from output")
	}
	require.Equal(t, wantInitialTokens, got,
		"first displayed token count should match BPE count of initial skill")
}

func makeSkill(name, description string) *skill.Skill {
	raw := "---\nname: " + name + "\ndescription: " + description + "\n---\n"
	return &skill.Skill{
		Frontmatter:    skill.Frontmatter{Name: name, Description: description},
		FrontmatterRaw: map[string]any{"name": name, "description": description},
		RawContent:     raw,
		Tokens:         tokens.Estimate(raw),
		Characters:     len(raw),
		Lines:          strings.Count(raw, "\n") + 1,
	}
}

func TestSuggestTriggers_NoDuplicates(t *testing.T) {
	sk := &skill.Skill{
		Frontmatter: skill.Frontmatter{
			Name: "my-tool",
		},
		Body: "No headings here.",
	}
	result := suggestTriggers(sk)
	phrases := parsePhrasesAfterPattern(result, "USE FOR:")
	seen := make(map[string]bool)
	for _, p := range phrases {
		require.False(t, seen[p], "duplicate trigger phrase: %s", p)
		seen[p] = true
	}
	require.Equal(t, 5, len(phrases), "should generate exactly 5 trigger phrases")
}

func parsePhrasesAfterPattern(text, pat string) []string {
	lower := strings.ToLower(text)
	patLower := strings.ToLower(pat)
	idx := strings.Index(lower, patLower)
	if idx < 0 {
		return nil
	}
	after := text[idx+len(pat):]
	for _, stop := range []string{"DO NOT USE FOR:", "INVOKES:", "FOR SINGLE OPERATIONS:", "\n\n"} {
		if si := strings.Index(strings.ToUpper(after), strings.ToUpper(stop)); si >= 0 {
			after = after[:si]
		}
	}
	segments := strings.Split(after, ",")
	var phrases []string
	for _, segment := range segments {
		candidate := strings.TrimSpace(segment)
		candidate = strings.TrimRight(candidate, ".")
		candidate = strings.Trim(candidate, "\"'`")
		if candidate == "" {
			continue
		}
		phrases = append(phrases, candidate)
	}
	return phrases
}
