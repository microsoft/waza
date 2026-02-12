package tokens

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCount_TableFormat(t *testing.T) {
	cmd := newCountCmd()
	cmd.SetArgs([]string{"testdata/count"})
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `File                                Tokens     Chars   Lines
------------------------------------------------------------
testdata/count/README.md                 7        27       1
testdata/count/SKILL.md                402      1608      83
testdata/count/references/one.md         9        35       1
testdata/count/references/two.md        10        40       1
------------------------------------------------------------
Total                                  428      1710      86

4 file(s) scanned
`
	require.Equal(t, expected, out.String())
}

func TestCount_JSONFormat(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "testdata/count"})
	require.NoError(t, cmd.Execute())

	output := out.String()

	var result countJSONOutput
	require.NoError(t, json.Unmarshal([]byte(output), &result), "invalid JSON output: %s", output)

	expectedFiles := []string{"testdata/count/README.md", "testdata/count/SKILL.md", "testdata/count/references/one.md", "testdata/count/references/two.md"}
	require.Equal(t, len(expectedFiles), result.TotalFiles)
	require.Equal(t, 428, result.TotalTokens) // 7 + 402 + 9 + 10

	for _, f := range expectedFiles {
		require.Contains(t, result.Files, f)
	}

	expected := map[string]countFileEntry{
		"testdata/count/README.md":         {Tokens: 7, Characters: 27, Lines: 1},
		"testdata/count/SKILL.md":          {Tokens: 402, Characters: 1608, Lines: 83},
		"testdata/count/references/one.md": {Tokens: 9, Characters: 35, Lines: 1},
		"testdata/count/references/two.md": {Tokens: 10, Characters: 40, Lines: 1},
	}
	for file, want := range expected {
		got := result.Files[file]
		require.Equal(t, want.Tokens, got.Tokens, "%s tokens", file)
		require.Equal(t, want.Characters, got.Characters, "%s characters", file)
		require.Equal(t, want.Lines, got.Lines, "%s lines", file)
	}

	require.NotContains(t, result.Files, "testdata/count/scripts/sample.py")
}

func TestCount_SortByTokens(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--sort", "tokens", "testdata/count"})
	require.NoError(t, cmd.Execute())

	lines := strings.Split(out.String(), "\n")
	var dataLines []string
	for _, line := range lines {
		if strings.Contains(line, ".md") {
			dataLines = append(dataLines, line)
		}
	}
	require.GreaterOrEqual(t, len(dataLines), 2)
	require.Contains(t, dataLines[0], "SKILL.md", "SKILL.md should be first when sorted by tokens")
}

func TestCount_SortByName(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--sort", "name", "testdata/count"})
	require.NoError(t, cmd.Execute())

	output := out.String()

	lines := strings.Split(output, "\n")
	var dataLines []string
	for _, line := range lines {
		if strings.Contains(line, ".md") {
			dataLines = append(dataLines, line)
		}
	}

	require.GreaterOrEqual(t, len(dataLines), 2)
	require.Contains(t, dataLines[0], "one.md", "first file alphabetically by name")
}

func TestCount_MinTokens(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "--min-tokens", "100", "testdata/count"})
	require.NoError(t, cmd.Execute())

	var result countJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	for file, entry := range result.Files {
		require.GreaterOrEqual(t, entry.Tokens, 100, "%s should have >= 100 tokens", file)
	}
}

func TestCount_NoTotal(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--no-total", "testdata/count"})
	require.NoError(t, cmd.Execute())

	output := out.String()

	require.NotContains(t, output, "Total")
	require.NotContains(t, output, "file(s) scanned")
}

func TestCount_SpecificPath(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", filepath.Join("testdata", "count", "SKILL.md")})
	require.NoError(t, cmd.Execute())

	var result countJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Equal(t, 1, result.TotalFiles)
	require.Equal(t, 402, result.TotalTokens)

	require.Contains(t, result.Files, "testdata/count/SKILL.md")
	entry := result.Files["testdata/count/SKILL.md"]
	require.Equal(t, 402, entry.Tokens)
	require.Equal(t, 1608, entry.Characters)
	require.Equal(t, 83, entry.Lines)
}

func TestCount_DirectoryPath(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", filepath.Join("testdata", "count", "references")})
	require.NoError(t, cmd.Execute())

	var result countJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Equal(t, 2, result.TotalFiles)
	require.Equal(t, 19, result.TotalTokens) // 9 + 10
}

func TestCount_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	output := out.String()

	require.Contains(t, output, "No markdown files found")
}

func TestCount_NonMarkdownFilesExcluded(t *testing.T) {
	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{filepath.Join("testdata", "count", "scripts")})
	require.NoError(t, cmd.Execute())

	output := out.String()

	require.Contains(t, output, "No markdown files found")
}

func TestCount_AbsoluteDirectoryPath(t *testing.T) {
	absDir, err := filepath.Abs(filepath.Join("testdata", "count", "references"))
	require.NoError(t, err)

	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", absDir})
	require.NoError(t, cmd.Execute())

	var result countJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Equal(t, 2, result.TotalFiles)
	require.Equal(t, 19, result.TotalTokens)
}

func TestCount_AbsoluteFilePath(t *testing.T) {
	absFile, err := filepath.Abs(filepath.Join("testdata", "count", "SKILL.md"))
	require.NoError(t, err)

	out := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", absFile})
	require.NoError(t, cmd.Execute())

	var result countJSONOutput
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Equal(t, 1, result.TotalFiles)
	require.Contains(t, result.Files, "testdata/count/SKILL.md")

	t.Run("multiple files", func(t *testing.T) {
		a, err := filepath.Abs(filepath.Join("testdata", "count", "SKILL.md"))
		require.NoError(t, err)
		b, err := filepath.Abs(filepath.Join("testdata", "count", "references", "one.md"))
		require.NoError(t, err)
		out := new(bytes.Buffer)
		cmd := newCountCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"--format", "json", a, b})
		require.NoError(t, cmd.Execute())

		var result countJSONOutput
		require.NoError(t, json.Unmarshal(out.Bytes(), &result))

		require.Equal(t, 2, result.TotalFiles)
		require.Contains(t, result.Files, "testdata/count/SKILL.md")
		require.Contains(t, result.Files, "testdata/count/references/one.md")
	})
}

func TestCount_SortWithJSONErrors(t *testing.T) {
	cmd := newCountCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--format", "json", "--sort", "tokens", "testdata/count"})
	require.ErrorContains(t, cmd.Execute(), "--sort is only supported with table output")
}

func TestCount_NoTotalWithJSONErrors(t *testing.T) {
	cmd := newCountCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--format", "json", "--no-total", "testdata/count"})
	require.ErrorContains(t, cmd.Execute(), "--no-total is only supported with table output")
}

func TestCount_NonexistentPath(t *testing.T) {
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	cmd := newCountCmd()
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"no-such-dir"})
	require.ErrorContains(t, cmd.Execute(), "no-such-dir")
}
