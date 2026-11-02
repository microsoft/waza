package graders

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCompiledRegexSliceSemantics(t *testing.T) {
	require.Nil(t, compileRegexes(nil))
	require.Nil(t, regexSources(nil))

	compiled := compileRegexes([]string{})
	require.NotNil(t, compiled)
	require.Empty(t, compiled)

	sources := regexSources([]compiledRegex{})
	require.NotNil(t, sources)
	require.Empty(t, sources)
}

func TestTextGraderRegexDetailsPreserveNilAndEmpty(t *testing.T) {
	for _, test := range []struct {
		name     string
		patterns []string
		wantJSON string
	}{
		{name: "nil", patterns: nil, wantJSON: "null"},
		{name: "empty", patterns: []string{}, wantJSON: "[]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			grader, err := NewTextGrader("text", models.TextGraderParameters{
				Contains:   []string{"ok"},
				RegexMatch: test.patterns,
			})
			require.NoError(t, err)

			result, err := grader.Grade(t.Context(), &Context{Output: "ok"})
			require.NoError(t, err)
			encoded, err := json.Marshal(result.Details["regex_match"])
			require.NoError(t, err)
			require.JSONEq(t, test.wantJSON, string(encoded))
		})
	}
}
