package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokensCheckProcessExitCodes(t *testing.T) {
	binary := buildOfflineComparisonCLI(t)
	fixture := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixture, ".waza.yaml"), []byte(`tokens:
  limits:
    defaults:
      "*.md": 1
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "big.md"), []byte("# This has more than one token\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "empty.md"), nil, 0o600))

	run := func(t *testing.T, dir string, args ...string) ([]byte, []byte, int) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "WAZA_NO_UPDATE_CHECK=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err == nil {
			return stdout.Bytes(), stderr.Bytes(), 0
		}
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "stderr=%s", stderr.String())
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
	}

	t.Run("advisory over budget exits zero", func(t *testing.T) {
		stdout, stderr, code := run(t, fixture, "tokens", "check", "big.md", "--format", "json")
		require.Equal(t, 0, code)
		require.Empty(t, stderr)
		assertExceededTokenReport(t, stdout)
	})

	t.Run("strict over budget exits one", func(t *testing.T) {
		stdout, stderr, code := run(t, fixture, "tokens", "check", "big.md", "--strict", "--format", "json")
		require.Equal(t, 1, code)
		require.Empty(t, stdout)
		assertExceededTokenReport(t, stderr)
	})

	t.Run("invalid input exits two", func(t *testing.T) {
		stdout, stderr, code := run(t, fixture, "tokens", "check", filepath.Join("missing", "note.md"), "--format", "json")
		require.Equal(t, 2, code)
		require.Empty(t, stdout)
		require.NotEmpty(t, stderr)
	})

	t.Run("strict within budget exits zero", func(t *testing.T) {
		stdout, stderr, code := run(t, fixture, "tokens", "check", "empty.md", "--strict", "--format", "json")
		require.Equal(t, 0, code)
		require.Empty(t, stderr)
		var report struct {
			TotalFiles    int `json:"totalFiles"`
			ExceededCount int `json:"exceededCount"`
			Results       []struct {
				File     string `json:"file"`
				Tokens   int    `json:"tokens"`
				Exceeded bool   `json:"exceeded"`
			} `json:"results"`
		}
		require.NoError(t, json.Unmarshal(stdout, &report))
		require.Equal(t, 1, report.TotalFiles)
		require.Zero(t, report.ExceededCount)
		require.Len(t, report.Results, 1)
		require.Equal(t, "empty.md", report.Results[0].File)
		require.Zero(t, report.Results[0].Tokens)
		require.False(t, report.Results[0].Exceeded)
	})

	t.Run("multi skill workspace preserves advisory and strict exits", func(t *testing.T) {
		workspace := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ".waza.yaml"), []byte(`paths:
  skills: skills
tokens:
  limits:
    defaults:
      "*.md": 1
`), 0o600))
		for _, name := range []string{"skill-a", "skill-b"} {
			dir := filepath.Join(workspace, "skills", name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: `+name+`
description: multi-skill exit-code fixture
---
# This has more than one token
`), 0o600))
		}

		stdout, stderr, code := run(t, workspace, "tokens", "check", "--format", "json")
		require.Equal(t, 0, code)
		require.Empty(t, stderr)
		assertExceededWorkspaceReport(t, stdout)

		stdout, stderr, code = run(t, workspace, "tokens", "check", "--strict", "--format", "json")
		require.Equal(t, 1, code)
		assertExceededWorkspaceReport(t, stdout)
		require.Contains(t, string(stderr), "one or more skills have files exceeding token limits")
	})

	t.Run("strict mixed read failure and budget excess exits two", func(t *testing.T) {
		workspace := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workspace, ".waza.yaml"), []byte(`paths:
  skills: skills
tokens:
  limits:
    defaults:
      "*.md": 1
`), 0o600))
		for _, name := range []string{"good-skill", "bad-skill"} {
			dir := filepath.Join(workspace, "skills", name)
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(
				"---\nname: "+name+"\ndescription: mixed-error fixture\n---\n# More than one token\n"), 0o600))
		}
		require.NoError(t, os.Symlink("absent.md", filepath.Join(workspace, "skills", "bad-skill", "dangling.md")))
		for _, strict := range []bool{false, true} {
			args := []string{"tokens", "check", "--format", "json"}
			wantCode := 0
			if strict {
				args = append(args, "--strict")
				wantCode = 2
			}
			stdout, stderr, code := run(t, workspace, args...)
			require.Equal(t, wantCode, code)
			require.Contains(t, string(stderr), "bad-skill")
			require.Contains(t, string(stderr), "dangling.md")
			var report struct {
				Skills []struct {
					Skill         string `json:"skill"`
					ExceededCount int    `json:"exceededCount"`
					Error         string `json:"error"`
				} `json:"skills"`
			}
			require.NoError(t, json.Unmarshal(stdout, &report))
			require.Len(t, report.Skills, 2)
			require.ElementsMatch(t, []string{"good-skill", "bad-skill"},
				[]string{report.Skills[0].Skill, report.Skills[1].Skill})
			for _, skill := range report.Skills {
				switch skill.Skill {
				case "good-skill":
					require.Equal(t, 1, skill.ExceededCount)
					require.Empty(t, skill.Error)
				case "bad-skill":
					require.Contains(t, skill.Error, "dangling.md")
				default:
					t.Fatalf("unexpected skill %q", skill.Skill)
				}
			}
		}
	})
}

func assertExceededTokenReport(t *testing.T, data []byte) {
	t.Helper()
	var report struct {
		TotalFiles    int `json:"totalFiles"`
		ExceededCount int `json:"exceededCount"`
		Results       []struct {
			File     string `json:"file"`
			Exceeded bool   `json:"exceeded"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(data, &report), "%s", data)
	require.Equal(t, 1, report.TotalFiles)
	require.Equal(t, 1, report.ExceededCount)
	require.Len(t, report.Results, 1)
	require.Equal(t, "big.md", report.Results[0].File)
	require.True(t, report.Results[0].Exceeded)
}

func assertExceededWorkspaceReport(t *testing.T, data []byte) {
	t.Helper()
	var report struct {
		Skills []struct {
			Skill         string `json:"skill"`
			TotalFiles    int    `json:"totalFiles"`
			ExceededCount int    `json:"exceededCount"`
			Results       []struct {
				Exceeded bool `json:"exceeded"`
			} `json:"results"`
		} `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(data, &report), "%s", data)
	require.Len(t, report.Skills, 2)
	for _, skill := range report.Skills {
		require.Contains(t, []string{"skill-a", "skill-b"}, skill.Skill)
		require.Equal(t, 1, skill.TotalFiles)
		require.Equal(t, 1, skill.ExceededCount)
		require.Len(t, skill.Results, 1)
		require.True(t, skill.Results[0].Exceeded)
	}
}
