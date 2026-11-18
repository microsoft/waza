package snapshot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixturePolicyPreservesOnlyExactOriginalTokens(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const email = "fixture@example.invalid"
	for _, test := range []struct {
		input    string
		preserve bool
	}{
		{sha, true},
		{"(" + sha + ")", true},
		{"commit: " + sha + "\r\n", true},
		{email, true},
		{sha + "aa", false},
		{"prefix-" + sha, false},
		{sha + "/suffix", false},
		{"prefix=" + sha, false},
		{"é" + sha, false},
		{sha + "\u0301", false},
		{"prefix." + email, false},
		{"AKIAIOSFODNN7EXAMPLE" + email, false},
		{"Bearer " + sha, false},
		{strings.Repeat("b", 40), false},
		{"private@example.invalid", false},
	} {
		t.Run(test.input, func(t *testing.T) {
			p, err := DefaultPolicyWithNonSecretLiterals([]string{sha, email})
			require.NoError(t, err)
			output := p.RedactString(test.input)
			if test.preserve {
				require.Equal(t, test.input, output)
				require.Zero(t, p.MatchCount())
				require.Positive(t, p.PreservedCount())
			} else {
				require.NotEqual(t, test.input, output)
				require.Positive(t, p.MatchCount())
				for _, literal := range []string{sha, email} {
					if strings.Contains(test.input, literal) {
						require.NotContains(t, output, literal)
					}
				}
			}
		})
	}
}

func TestDefaultFixturePolicyRetainsFrozenRedactionContract(t *testing.T) {
	const input = `{"contact":"fixture@example.invalid","projectKey":"PROJ-1","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	const want = `{"contact":"[REDACTED]","projectKey":"[REDACTED]","sha":"[REDACTED]"}`
	for _, policy := range []*Policy{DefaultPolicy(), func() *Policy {
		p, err := DefaultPolicyWithNonSecretLiterals(nil)
		require.NoError(t, err)
		return p
	}()} {
		var value any
		require.NoError(t, json.Unmarshal([]byte(input), &value))
		encoded, err := json.Marshal(policy.RedactAny(value))
		require.NoError(t, err)
		require.Equal(t, want, string(encoded))
		require.Equal(t, 2, policy.MatchCount(), "legacy RedactAny counts regex matches only")
		require.Equal(t, []string{"azure_openai_key", "email"}, policy.MatchedRules())
	}
}
