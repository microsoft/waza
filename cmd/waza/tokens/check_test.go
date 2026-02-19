package tokens

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// checkFixture returns the absolute path to a testdata/check subdirectory.
func checkFixture(t *testing.T, name string) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("testdata", "check", name))
	require.NoError(t, err)
	return d
}

func TestCheck_AllWithinLimit(t *testing.T) {
	td := checkFixture(t, "all-pass")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `File               Tokens  Limit  Status
--------------------------------------------------
README.md               6  10000  ✅ OK
SKILL.md              424  10000  ✅ OK
references/one.md       6  10000  ✅ OK
references/two.md       6  10000  ✅ OK
--------------------------------------------------

4/4 files within limits
`
	require.Equal(t, expected, out.String())
}

func TestCheck_SomeExceedLimit(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `File               Tokens  Limit  Status
--------------------------------------------------
SKILL.md              424    100  ❌ EXCEEDED
README.md               6    100  ✅ OK
references/one.md       6    100  ✅ OK
references/two.md       6    100  ✅ OK
--------------------------------------------------

3/4 files within limits

⚠️  1 file(s) exceed their token limits:
   SKILL.md: 424 tokens (324 over limit of 100)
`
	require.Equal(t, expected, out.String())
}

func TestCheck_StrictFails(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	cmd := newCheckCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--strict"})
	err := cmd.Execute()
	require.Error(t, err)

	expected := `File               Tokens  Limit  Status
--------------------------------------------------
SKILL.md              424    100  ❌ EXCEEDED
README.md               6    100  ✅ OK
references/one.md       6    100  ✅ OK
references/two.md       6    100  ✅ OK
--------------------------------------------------

3/4 files within limits

⚠️  1 file(s) exceed their token limits:
   SKILL.md: 424 tokens (324 over limit of 100)
`
	require.Equal(t, expected, err.Error())
}

func TestCheck_StrictPassesWhenWithinLimit(t *testing.T) {
	td := checkFixture(t, "all-pass")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--strict"})
	require.NoError(t, cmd.Execute())
}

func TestCheck_JSONFormat(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var report checkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	require.Equal(t, 4, report.TotalFiles)
	require.Greater(t, report.ExceededCount, 0)

	for _, r := range report.Results {
		require.Equal(t, 100, r.Limit)
		if r.Tokens > 100 {
			require.True(t, r.Exceeded)
		}
	}
}

func TestCheck_JSONAllPass(t *testing.T) {
	td := checkFixture(t, "all-pass")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var report checkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	require.Equal(t, 0, report.ExceededCount)
	for _, r := range report.Results {
		require.False(t, r.Exceeded)
	}
}

func TestCheck_Quiet(t *testing.T) {
	td := checkFixture(t, "all-pass")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--quiet"})
	require.NoError(t, cmd.Execute())
	require.Empty(t, out.String())
}

func TestCheck_QuietWithExceeded(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--quiet"})
	require.NoError(t, cmd.Execute())
	require.Empty(t, out.String())
}

func TestCheck_QuietStrictWithExceeded(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	cmd := newCheckCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"--quiet", "--strict"})
	err := cmd.Execute()
	require.Error(t, err)

	expected := `File               Tokens  Limit  Status
--------------------------------------------------
SKILL.md              424    100  ❌ EXCEEDED
README.md               6    100  ✅ OK
references/one.md       6    100  ✅ OK
references/two.md       6    100  ✅ OK
--------------------------------------------------

3/4 files within limits

⚠️  1 file(s) exceed their token limits:
   SKILL.md: 424 tokens (324 over limit of 100)
`
	require.Equal(t, expected, err.Error())
}

func TestCheck_SpecificFile(t *testing.T) {
	td := checkFixture(t, "all-pass")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "SKILL.md"})
	require.NoError(t, cmd.Execute())

	var report checkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	require.Equal(t, 1, report.TotalFiles)
	require.Len(t, report.Results, 1)
	require.True(t, strings.HasSuffix(report.Results[0].File, "SKILL.md"))
}

func TestCheck_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	require.Equal(t, "No markdown files found.", out.String())
}

func TestCheck_ExceededSortedFirst(t *testing.T) {
	td := checkFixture(t, "some-exceed")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var report checkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	sawPassing := false
	for _, r := range report.Results {
		if !r.Exceeded {
			sawPassing = true
		}
		if sawPassing {
			require.False(t, r.Exceeded, "exceeded files should come before passing files")
		}
	}
}

func TestCheck_NonexistentPath(t *testing.T) {
	cmd := newCheckCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetArgs([]string{"no-such-dir"})
	require.ErrorContains(t, cmd.Execute(), "no-such-dir")
}

func TestCheck_ConfigLimits(t *testing.T) {
	td := checkFixture(t, "overrides")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var report checkReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))

	require.Equal(t, 2, report.TotalFiles)

	limitsByFile := make(map[string]int)
	for _, r := range report.Results {
		limitsByFile[r.File] = r.Limit
	}
	require.Equal(t, 10, limitsByFile["normal.md"])
	require.Equal(t, 5000, limitsByFile["special.md"])
}

func TestCheck_DefaultLimitsWhenNoConfig(t *testing.T) {
	td := checkFixture(t, "no-config")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())

	expected := `File       Tokens  Limit  Status
------------------------------------------
README.md       4   3000  ✅ OK
SKILL.md        3    500  ✅ OK
------------------------------------------

2/2 files within limits
`
	require.Equal(t, expected, out.String())
}

func TestCheck_ConfigPatternInJSON(t *testing.T) {
	td := checkFixture(t, "pattern")
	t.Chdir(td)

	out := new(bytes.Buffer)
	cmd := newCheckCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json"})
	require.NoError(t, cmd.Execute())

	var result struct {
		Results []struct {
			Pattern string `json:"pattern"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Len(t, result.Results, 1)
	require.Equal(t, "*.md", result.Results[0].Pattern)
}
