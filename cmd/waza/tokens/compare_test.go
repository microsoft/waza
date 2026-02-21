package tokens

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	cmds := [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", msg},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "failed to run: %v", args)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "failed to run: %v", args)
	}
	return dir
}

func TestCompare_NotGitRepo(t *testing.T) {
	t.Chdir(t.TempDir())

	out := new(bytes.Buffer)
	cmd := newCompareCmd()
	cmd.SetOut(out)
	cmd.SetErr(new(bytes.Buffer))

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a git repository")
}

func TestCompare(t *testing.T) {
	dir := initRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unchanged.md"), []byte("# V1"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "references"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "references", "spec.md"), []byte("this is reference content"), 0o644))

	t.Run("added files", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)

		require.NoError(t, cmd.Execute())
		expected := "\n📊 Token Comparison: HEAD → WORKING\n\n" +
			"File                  Before     After      Diff  Status\n" +
			"------------------------------------------------------------------\n" +
			"README.md                  -         3        +3  🆕\n" +
			"references/spec.md         -         4        +4  🆕\n" +
			"unchanged.md               -         3        +3  🆕\n" +
			"------------------------------------------------------------------\n" +
			"Total                      0        10       +10  100.0%\n" +
			"\n📋 Summary:\n" +
			"   Added: 3, Removed: 0, Modified: 0\n" +
			"   Increased: 3, Decreased: 0\n"
		require.Equal(t, expected, out.String())
	})

	commit(t, dir, "commit1")

	t.Run("no changes", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)

		require.NoError(t, cmd.Execute())
		require.Equal(t, "No changes detected.\n", out.String())
	})

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V2 with more content here"), 0o644))
	commit(t, dir, "commit2")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V3 is like V2 but has even more content"), 0o644))
	commit(t, dir, "commit3")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V4 is the most content-rich version you've ever seen and it keeps going with even more words to ensure a different count"), 0o644))

	for _, test := range []struct {
		args          []string
		expectedTable string
	}{
		{
			args: []string{"HEAD~2", "HEAD~1"},
			expectedTable: "\n📊 Token Comparison: HEAD~2 → HEAD~1\n\n" +
				"File         Before     After      Diff  Status\n" +
				"---------------------------------------------------------\n" +
				"README.md         3         7        +4  📈\n" +
				"---------------------------------------------------------\n" +
				"Total            10        14        +4  40.0%\n" +
				"\n📋 Summary:\n" +
				"   Added: 0, Removed: 0, Modified: 1\n" +
				"   Increased: 1, Decreased: 0\n",
		},
		{
			args: []string{"HEAD~2", "HEAD"},
			expectedTable: "\n📊 Token Comparison: HEAD~2 → HEAD\n\n" +
				"File         Before     After      Diff  Status\n" +
				"---------------------------------------------------------\n" +
				"README.md         3        12        +9  📈\n" +
				"---------------------------------------------------------\n" +
				"Total            10        19        +9  90.0%\n" +
				"\n📋 Summary:\n" +
				"   Added: 0, Removed: 0, Modified: 1\n" +
				"   Increased: 1, Decreased: 0\n",
		},
		{
			args: []string{"HEAD~1", "HEAD"},
			expectedTable: "\n📊 Token Comparison: HEAD~1 → HEAD\n\n" +
				"File         Before     After      Diff  Status\n" +
				"---------------------------------------------------------\n" +
				"README.md         7        12        +5  📈\n" +
				"---------------------------------------------------------\n" +
				"Total            14        19        +5  35.7%\n" +
				"\n📋 Summary:\n" +
				"   Added: 0, Removed: 0, Modified: 1\n" +
				"   Increased: 1, Decreased: 0\n",
		},
		{
			args: []string{"HEAD"},
			expectedTable: "\n📊 Token Comparison: HEAD → WORKING\n\n" +
				"File         Before     After      Diff  Status\n" +
				"---------------------------------------------------------\n" +
				"README.md        12        25       +13  📈\n" +
				"---------------------------------------------------------\n" +
				"Total            19        32       +13  68.4%\n" +
				"\n📋 Summary:\n" +
				"   Added: 0, Removed: 0, Modified: 1\n" +
				"   Increased: 1, Decreased: 0\n",
		},
		{
			expectedTable: "\n📊 Token Comparison: HEAD → WORKING\n\n" +
				"File         Before     After      Diff  Status\n" +
				"---------------------------------------------------------\n" +
				"README.md        12        25       +13  📈\n" +
				"---------------------------------------------------------\n" +
				"Total            19        32       +13  68.4%\n" +
				"\n📋 Summary:\n" +
				"   Added: 0, Removed: 0, Modified: 1\n" +
				"   Increased: 1, Decreased: 0\n",
		},
	} {
		name := strings.Join(test.args, "->")
		if name == "" {
			name = "ref unspecified"
		}
		t.Run(name, func(t *testing.T) {
			out := new(bytes.Buffer)
			cmd := newCompareCmd()
			cmd.SetOut(out)
			cmd.SetArgs(test.args)
			require.NoError(t, cmd.Execute())

			require.Equal(t, test.expectedTable, out.String())
		})
	}

	t.Run("show-unchanged", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"--show-unchanged"})

		require.NoError(t, cmd.Execute())
		expected := "\n📊 Token Comparison: HEAD → WORKING\n\n" +
			"File                  Before     After      Diff  Status\n" +
			"------------------------------------------------------------------\n" +
			"README.md                 12        25       +13  📈\n" +
			"references/spec.md         4         4         0  ➡️\n" +
			"unchanged.md               3         3         0  ➡️\n" +
			"------------------------------------------------------------------\n" +
			"Total                     19        32       +13  68.4%\n" +
			"\n📋 Summary:\n" +
			"   Added: 0, Removed: 0, Modified: 1\n" +
			"   Increased: 1, Decreased: 0\n"
		require.Equal(t, expected, out.String())
	})

	require.NoError(t, os.Remove(filepath.Join(dir, "unchanged.md")))
	t.Run("removed file", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)

		require.NoError(t, cmd.Execute())
		expected := "\n📊 Token Comparison: HEAD → WORKING\n\n" +
			"File            Before     After      Diff  Status\n" +
			"------------------------------------------------------------\n" +
			"README.md           12        25       +13  📈\n" +
			"unchanged.md         3         -        -3  🗑️\n" +
			"------------------------------------------------------------\n" +
			"Total               19        29       +10  52.6%\n" +
			"\n📋 Summary:\n" +
			"   Added: 0, Removed: 1, Modified: 1\n" +
			"   Increased: 1, Decreased: 1\n"
		require.Equal(t, expected, out.String())
	})

	t.Run("json", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"--format", "json"})

		require.NoError(t, cmd.Execute())

		var report comparisonReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))

		require.Equal(t, "HEAD", report.BaseRef)
		require.Equal(t, "WORKING", report.HeadRef)
		require.Equal(t, 1, report.Summary.FilesModified)
		require.Equal(t, 1, report.Summary.FilesRemoved)

		foundReadme := false
		foundUnchanged := false
		for _, f := range report.Files {
			switch f.File {
			case "README.md":
				foundReadme = true
				require.Equal(t, "modified", f.Status)
				require.NotNil(t, f.Before)
				require.NotNil(t, f.After)
				require.Equal(t, 12, f.Before.Tokens)
				require.Equal(t, 25, f.After.Tokens)
			case "unchanged.md":
				foundUnchanged = true
				require.Equal(t, "removed", f.Status)
				require.NotNil(t, f.Before)
				require.Nil(t, f.After)
				require.Equal(t, 3, f.Before.Tokens)
			}
		}
		require.True(t, foundReadme, "README.md should be in results")
		require.True(t, foundUnchanged, "unchanged.md should be in results")
	})
}

func TestCompare_Branches(t *testing.T) {
	dir := initRepo(t)

	// Initial commit on main
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o644))
	commit(t, dir, "initial")

	// Create branch-a with additional content
	gitCmd := exec.Command("git", "checkout", "-b", "branch-a")
	gitCmd.Dir = dir
	require.NoError(t, gitCmd.Run())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello from branch A with extra content"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature-a.md"), []byte("# Feature A"), 0o644))
	commit(t, dir, "branch-a changes")

	// Create branch-b from main with different content
	gitCmd = exec.Command("git", "checkout", "main", "-b", "branch-b")
	gitCmd.Dir = dir
	require.NoError(t, gitCmd.Run())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello from branch B with different and longer content added"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature-b.md"), []byte("# Feature B docs"), 0o644))
	commit(t, dir, "branch-b changes")

	t.Run("table", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"branch-a", "branch-b"})

		require.NoError(t, cmd.Execute())
		expected := "\n📊 Token Comparison: branch-a → branch-b\n\n" +
			"File            Before     After      Diff  Status\n" +
			"------------------------------------------------------------\n" +
			"README.md            8        11        +3  📈\n" +
			"feature-a.md         3         -        -3  🗑️\n" +
			"feature-b.md         -         4        +4  🆕\n" +
			"------------------------------------------------------------\n" +
			"Total               11        15        +4  36.4%\n" +
			"\n📋 Summary:\n" +
			"   Added: 1, Removed: 1, Modified: 1\n" +
			"   Increased: 2, Decreased: 1\n"
		require.Equal(t, expected, out.String())
	})

	t.Run("json", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"branch-a", "branch-b", "--format", "json"})

		require.NoError(t, cmd.Execute())

		var report comparisonReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))

		require.Equal(t, "branch-a", report.BaseRef)
		require.Equal(t, "branch-b", report.HeadRef)

		byFile := make(map[string]fileComparison)
		for _, f := range report.Files {
			byFile[f.File] = f
		}

		// feature-a.md exists only in branch-a → removed
		fa, ok := byFile["feature-a.md"]
		require.True(t, ok, "feature-a.md should be in results")
		require.Equal(t, "removed", fa.Status)
		require.NotNil(t, fa.Before)
		require.Nil(t, fa.After)

		// feature-b.md exists only in branch-b → added
		fb, ok := byFile["feature-b.md"]
		require.True(t, ok, "feature-b.md should be in results")
		require.Equal(t, "added", fb.Status)
		require.Nil(t, fb.Before)
		require.NotNil(t, fb.After)

		// README.md modified between branches
		readme, ok := byFile["README.md"]
		require.True(t, ok, "README.md should be in results")
		require.Equal(t, "modified", readme.Status)
		require.NotNil(t, readme.Before)
		require.NotNil(t, readme.After)
	})
}

// TestCompare_InvalidRef verifies that an invalid/nonexistent ref does not cause
// a hard error. The TypeScript implementation catches all git errors and returns
// empty results; the Go implementation should behave the same way.
func TestCompare_InvalidRef(t *testing.T) {
	dir := initRepo(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o644))
	commit(t, dir, "initial")

	t.Run("nonexistent base ref", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"nonexistent-ref", "main"})

		// Should succeed, treating all files in main as added
		require.NoError(t, cmd.Execute())
		require.Contains(t, out.String(), "README.md")
		require.Contains(t, out.String(), "🆕")
	})

	t.Run("nonexistent head ref", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"main", "nonexistent-ref"})

		// Should succeed, treating all files in main as removed
		require.NoError(t, cmd.Execute())
		require.Contains(t, out.String(), "README.md")
		require.Contains(t, out.String(), "🗑️")
	})

	t.Run("both refs nonexistent", func(t *testing.T) {
		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"bad-ref-1", "bad-ref-2"})

		require.NoError(t, cmd.Execute())
		require.Equal(t, "No changes detected.\n", out.String())
	})
}

func TestCompare_Strict(t *testing.T) {
	t.Run("passes when under limit", func(t *testing.T) {
		dir := initRepo(t)

		// Set a generous limit
		limits := `{"defaults": {"*.md": 100}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), []byte(limits), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0o644))
		commit(t, dir, "initial")

		// Modify but stay under limit
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello World"), 0o644))

		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"--strict"})

		require.NoError(t, cmd.Execute())
		require.NotContains(t, out.String(), "exceed token limits")
	})

	t.Run("fails when over limit", func(t *testing.T) {
		dir := initRepo(t)

		// Set a very low limit
		limits := `{"defaults": {"*.md": 3}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), []byte(limits), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V1"), 0o644))
		commit(t, dir, "initial")

		// Modify to exceed limit (>3 tokens)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V2 with more content here"), 0o644))

		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetErr(new(bytes.Buffer))
		cmd.SetArgs([]string{"--strict"})

		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), "exceed token limits")
		require.Contains(t, out.String(), "⚠️")
		require.Contains(t, out.String(), "exceed token limits")
	})

	t.Run("only checks after ref not removed files", func(t *testing.T) {
		dir := initRepo(t)

		// Set a very low limit
		limits := `{"defaults": {"*.md": 3}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), []byte(limits), 0o644))
		// This file has >3 tokens but will be removed
		require.NoError(t, os.WriteFile(filepath.Join(dir, "big.md"), []byte("# Big file with many tokens"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.md"), []byte("# OK"), 0o644))
		commit(t, dir, "initial")

		// Remove the big file — should not trigger exceeded
		require.NoError(t, os.Remove(filepath.Join(dir, "big.md")))

		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs([]string{"--strict"})

		require.NoError(t, cmd.Execute())
	})

	t.Run("json includes limit info", func(t *testing.T) {
		dir := initRepo(t)

		limits := `{"defaults": {"*.md": 3}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), []byte(limits), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V1"), 0o644))
		commit(t, dir, "initial")

		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V2 with extra content exceeding the limit"), 0o644))

		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetErr(new(bytes.Buffer))
		cmd.SetArgs([]string{"--strict", "--format", "json"})

		err := cmd.Execute()
		require.Error(t, err)

		var report comparisonReport
		require.NoError(t, json.Unmarshal(out.Bytes(), &report))
		require.Greater(t, report.Summary.ExceededCount, 0)

		found := false
		for _, f := range report.Files {
			if f.File == "README.md" {
				found = true
				require.True(t, f.Exceeded)
				require.Equal(t, 3, f.Limit)
			}
		}
		require.True(t, found, "README.md should be in results")
	})

	t.Run("without strict flag no limit check", func(t *testing.T) {
		dir := initRepo(t)

		// Set a very low limit — but don't use --strict
		limits := `{"defaults": {"*.md": 3}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), []byte(limits), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V1"), 0o644))
		commit(t, dir, "initial")

		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# V2 with more content here"), 0o644))

		out := new(bytes.Buffer)
		cmd := newCompareCmd()
		cmd.SetOut(out)
		cmd.SetArgs(nil)

		// Without --strict, should succeed even though file exceeds limit
		require.NoError(t, cmd.Execute())
		require.NotContains(t, out.String(), "exceed token limits")
	})
}
