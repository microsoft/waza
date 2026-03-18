package checks

import (
	"testing"

	"github.com/microsoft/waza/internal/scoring"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/tokens"
	"github.com/stretchr/testify/require"
)

var _ ComplianceChecker = (*TokenBudgetChecker)(nil)

func TestTokenBudgetChecker_WithinLimit(t *testing.T) {
	checker := &TokenBudgetChecker{}
	result, err := checker.Check(skill.Skill{Tokens: 100})
	require.NoError(t, err)
	require.True(t, result.Passed)
	require.Equal(t, "token-budget", result.Name)

	data, ok := result.Data.(*TokenBudgetData)
	require.True(t, ok)
	require.False(t, data.Exceeded)
	require.Equal(t, scoring.TokenSoftLimit, data.TokenLimit)
	require.Equal(t, 100, data.TokenCount)
}

func TestTokenBudgetChecker_ExceedsLimit(t *testing.T) {
	checker := &TokenBudgetChecker{Limit: 10}
	result, err := checker.Check(skill.Skill{Tokens: 50})
	require.NoError(t, err)
	require.False(t, result.Passed)

	data, ok := result.Data.(*TokenBudgetData)
	require.True(t, ok)
	require.True(t, data.Exceeded)
	require.Equal(t, 10, data.TokenLimit)
	require.Equal(t, 50, data.TokenCount)
}

func TestTokenBudgetChecker_CustomLimit(t *testing.T) {
	checker := &TokenBudgetChecker{Limit: 1000}
	result, err := checker.Check(skill.Skill{Tokens: 5})
	require.NoError(t, err)
	require.True(t, result.Passed)

	data, ok := result.Data.(*TokenBudgetData)
	require.True(t, ok)
	require.Equal(t, 1000, data.TokenLimit)
}

func TestTokenBudgetChecker_ZeroTokens(t *testing.T) {
	checker := &TokenBudgetChecker{}
	result, err := checker.Check(skill.Skill{})
	require.NoError(t, err)
	require.True(t, result.Passed)

	data, ok := result.Data.(*TokenBudgetData)
	require.True(t, ok)
	require.Equal(t, 0, data.TokenCount)
}

func TestTokenBudgetChecker_CountsRawContentWithSharedDefaultTokenizer(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{
			name: "LF",
			raw:  []byte("---\nname: token-budget-raw-content\ndescription: Counts raw content with the shared BPE tokenizer.\n---\n\n# Token Budget\n\nVerify that check and tokens count agree.\n"),
		},
		{
			name: "CRLF",
			raw:  []byte("---\r\nname: token-budget-raw-content\r\ndescription: Counts raw content with the shared BPE tokenizer.\r\n---\r\n\r\n# Token Budget\r\n\r\nVerify that check and tokens count agree.\r\n"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sk skill.Skill
			require.NoError(t, sk.UnmarshalText(tc.raw))

			counter, err := tokens.DefaultCounter()
			require.NoError(t, err)
			want := counter.Count(string(tc.raw))

			checker := &TokenBudgetChecker{}
			result, err := checker.Check(sk)
			require.NoError(t, err)

			data, ok := result.Data.(*TokenBudgetData)
			require.True(t, ok)
			require.Equal(t, want, data.TokenCount)
		})
	}
}
