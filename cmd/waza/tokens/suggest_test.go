package tokens

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spboyer/waza/internal/execution"
	"github.com/stretchr/testify/require"
)

// suggestFixture returns the absolute path to a testdata/suggest subdirectory.
func suggestFixture(t *testing.T, name string) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("testdata", "suggest", name))
	require.NoError(t, err)
	return d
}

func TestSuggest_NoIssues(t *testing.T) {
	td := suggestFixture(t, "no-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	require.Equal(t, "✅ No optimization suggestions found.\n", out.String())
}

func TestSuggest_ExcessiveEmojis(t *testing.T) {
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `
📄 emojis.md (33 tokens)
------------------------------------------------------------
  Line 1: Found 9 emojis (7 over recommended 2)
    💡 Remove decorative emojis that don't aid comprehension (~14 tokens)

  Total potential savings: ~14 tokens

📊 Summary: 1 file(s) with suggestions, ~14 potential token savings
`
	require.Equal(t, expected, out.String())
}

func TestSuggest_LargeCodeBlock(t *testing.T) {
	td := suggestFixture(t, "large-codeblock")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `
📄 bigcode.md (59 tokens)
------------------------------------------------------------
  Line 3: Code block with 20 lines (10 over 10)
    💡 Consider truncating example or moving to reference file (~160 tokens)

  Total potential savings: ~160 tokens

📊 Summary: 1 file(s) with suggestions, ~160 potential token savings
`
	require.Equal(t, expected, out.String())
}

func TestSuggest_LargeTable(t *testing.T) {
	td := suggestFixture(t, "large-table")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `
📄 bigtable.md (190 tokens)
------------------------------------------------------------
  Line 3: Table with 15 rows (5 over 10)
    💡 Consider summarizing or moving to reference file (~60 tokens)

  Total potential savings: ~60 tokens

📊 Summary: 1 file(s) with suggestions, ~60 potential token savings
`
	require.Equal(t, expected, out.String())
}

func TestSuggest_TableAtEOF(t *testing.T) {
	td := suggestFixture(t, "table-at-eof")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses []fileAnalysis `json:"analyses"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.NotEmpty(t, result.Analyses[0].Suggestions)
	require.Contains(t, result.Analyses[0].Suggestions[0].Issue, "Table with")
}

func TestSuggest_ExceedsLimit(t *testing.T) {
	td := suggestFixture(t, "exceeds-limit")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `
📄 big.md (19 tokens)
------------------------------------------------------------
  Line 1: File exceeds token limit (19/5)
    💡 Split content into multiple files or use reference documents

📊 Summary: 1 file(s) with suggestions
`
	require.Equal(t, expected, out.String())
}

func TestSuggest_JSONFormat(t *testing.T) {
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Timestamp             string         `json:"timestamp"`
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.Equal(t, "emojis.md", result.Analyses[0].File)
	require.Equal(t, 14, result.TotalPotentialSavings)
	require.Len(t, result.Analyses[0].Suggestions, 1)
	require.Contains(t, result.Analyses[0].Suggestions[0].Issue, "emojis")
}

func TestSuggest_JSONNoIssues(t *testing.T) {
	td := suggestFixture(t, "no-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Empty(t, result.Analyses)
	require.Equal(t, 0, result.TotalPotentialSavings)
}

func TestSuggest_MinSavingsFilter(t *testing.T) {
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--min-savings", "100"})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "✅ No optimization suggestions found.\n", out.String())
}

func TestSuggest_MinSavingsPartialFilter(t *testing.T) {
	analyses := []fileAnalysis{
		{
			File:   "test.md",
			Tokens: 500,
			Suggestions: []suggestion{
				{Line: 1, Issue: "small issue", Suggestion: "fix it", EstimatedSavings: 5},
				{Line: 10, Issue: "big issue", Suggestion: "fix it", EstimatedSavings: 50},
			},
			PotentialSavings: 55,
		},
	}

	filtered := filterSuggestions(analyses, 10)
	got := suggestionText(filtered)
	require.Contains(t, got, "~50 potential token savings")
	require.NotContains(t, got, "~55")
	require.NotContains(t, got, "small issue")
	require.Contains(t, got, "big issue")
}

func TestSuggest_MinSavingsJSON(t *testing.T) {
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "--min-savings", "100"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Empty(t, result.Analyses)
	require.Equal(t, 0, result.TotalPotentialSavings)
}

func TestSuggest_MinSavingsJSONPartialFilter(t *testing.T) {
	analyses := []fileAnalysis{
		{
			File:   "test.md",
			Tokens: 500,
			Suggestions: []suggestion{
				{Line: 1, Issue: "small issue", Suggestion: "fix it", EstimatedSavings: 5},
				{Line: 10, Issue: "big issue", Suggestion: "fix it", EstimatedSavings: 50},
			},
			PotentialSavings: 55,
		},
	}

	filtered := filterSuggestions(analyses, 10)
	got, err := suggestionJSON(filtered)
	require.NoError(t, err)

	var result struct {
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings"`
	}
	require.NoError(t, json.Unmarshal([]byte(got), &result))

	require.Len(t, result.Analyses, 1)
	require.Len(t, result.Analyses[0].Suggestions, 1)
	require.Equal(t, "big issue", result.Analyses[0].Suggestions[0].Issue)
	require.Equal(t, 50, result.Analyses[0].PotentialSavings)
	require.Equal(t, 50, result.TotalPotentialSavings)
}

func TestSuggest_MultipleIssues(t *testing.T) {
	td := suggestFixture(t, "multiple-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.GreaterOrEqual(t, len(result.Analyses[0].Suggestions), 3)
	require.Greater(t, result.TotalPotentialSavings, 0)
}

func TestSuggest_SpecificFile(t *testing.T) {
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "emojis.md"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses []fileAnalysis `json:"analyses"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.Equal(t, "emojis.md", result.Analyses[0].File)
}

func TestSuggest_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "✅ No optimization suggestions found.\n", out.String())
}

func TestSuggest_NonexistentPath(t *testing.T) {
	cmd := newSuggestCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"no-such-dir"})
	require.ErrorContains(t, cmd.Execute(), "no-such-dir")
}

func withMockEngine(t *testing.T) {
	t.Helper()
	orig := newChatEngine
	newChatEngine = func(modelID string) execution.AgentEngine {
		return execution.NewMockEngine(modelID)
	}
	t.Cleanup(func() { newChatEngine = orig })
}

func TestSuggest_CopilotText(t *testing.T) {
	withMockEngine(t)
	td := suggestFixture(t, "no-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--copilot"})
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "Mock response for:")
}

func TestSuggest_CopilotJSON(t *testing.T) {
	withMockEngine(t)
	td := suggestFixture(t, "no-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--copilot", "--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses []fileAnalysis `json:"analyses"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.Equal(t, "clean.md", result.Analyses[0].File)
	require.Contains(t, result.Analyses[0].CopilotSuggestions, "Mock response for:")
}

func TestSuggest_CopilotMultipleFiles(t *testing.T) {
	withMockEngine(t)
	td := suggestFixture(t, "with-emojis")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--copilot", "--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses []fileAnalysis `json:"analyses"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.NotEmpty(t, result.Analyses[0].CopilotSuggestions)
	require.Empty(t, result.Analyses[0].Suggestions, "copilot mode should not run heuristic analysis")
}

func TestSuggest_CopilotModel(t *testing.T) {
	withMockEngine(t)
	td := suggestFixture(t, "no-issues")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newSuggestCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--copilot", "--model", "custom-model", "--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Analyses []fileAnalysis `json:"analyses"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Len(t, result.Analyses, 1)
	require.Contains(t, result.Analyses[0].CopilotSuggestions, "Mock response for:")
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{
			name:  "short line unchanged",
			input: "hello world",
			width: 80,
			want:  "hello world",
		},
		{
			name:  "wraps long line",
			input: strings.Repeat("word ", 30),
			width: 40,
			want:  "word word word word word word word word\nword word word word word word word word\nword word word word word word word word\nword word word word word word",
		},
		{
			name:  "preserves code fences",
			input: "```go\nsome very long line that should not be wrapped at all because it is inside a code fence\n```",
			width: 40,
			want:  "```go\nsome very long line that should not be wrapped at all because it is inside a code fence\n```",
		},
		{
			name:  "preserves headings",
			input: "# This is a very long heading that should remain on one line no matter what happens",
			width: 40,
			want:  "# This is a very long heading that should remain on one line no matter what happens",
		},
		{
			name:  "preserves list items",
			input: "- This is a list item\n* Another list item",
			width: 40,
			want:  "- This is a list item\n* Another list item",
		},
		{
			name:  "preserves indented lines",
			input: "  indented line\n\ttab indented",
			width: 10,
			want:  "  indented line\n\ttab indented",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapText(tt.input, tt.width)
			require.Equal(t, tt.want, got)
		})
	}
}
