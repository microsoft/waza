package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectRequestedBoundaryAndDiscoveryPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, tools, mode string
		cwdAgent, skill   bool
	}{
		{"allowlist", "tools: [read]\n", "requested-allowlist", false, false},
		{"deny all", "tools: []\n", "requested-deny-all", false, false},
		{"absent tools", "", "", false, false},
		{"skill takes precedence", "tools: []\n", "", false, true},
		{"cwd takes precedence", "tools: [write]\n", "requested-deny-all", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, dir := writeSuite(t, validEval, validTask)
			cwd := t.TempDir()
			t.Chdir(cwd)
			agents := filepath.Join(dir, "agents")
			require.NoError(t, os.Mkdir(agents, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(agents, "example.agent.md"),
				[]byte("---\nname: example\ndescription: Local agent\n"+tc.tools+"---\nBody\n"), 0600))
			if tc.skill {
				require.NoError(t, os.WriteFile(filepath.Join(agents, "SKILL.md"),
					[]byte("---\nname: example\ndescription: Local skill\n---\nBody\n"), 0600))
			}
			if tc.cwdAgent {
				require.NoError(t, os.WriteFile(filepath.Join(cwd, "example.agent.md"),
					[]byte("---\nname: example\ndescription: First agent\ntools: []\n---\nBody\n"), 0600))
			}
			eval := strings.ReplaceAll(validEval, "  model: harness", "  model: harness\n  skill_directories: [agents]")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
			r := Inspect(path, Options{})
			require.False(t, r.Failed(false), "%+v", r.Diagnostics)
			if tc.mode == "" {
				require.False(t, hasDiagnostic(r, "boundary.requested_policy", Unresolved), "%+v", r.Diagnostics)
				return
			}
			require.True(t, hasDiagnostic(r, "boundary.requested_policy", Unresolved), "%+v", r.Diagnostics)
			require.True(t, r.Failed(true))
			selected := filepath.Join(agents, "example.agent.md")
			if tc.cwdAgent {
				selected = filepath.Join(cwd, "example.agent.md")
			}
			require.Contains(t, r.Dependencies, Dependency{Kind: "boundary", Name: selected, Mode: tc.mode, State: Unresolved, TaskID: "task"})
		})
	}
}

func TestInspectPostRunBoundaryDoesNotPromisePrevention(t *testing.T) {
	path, _ := writeSuite(t, validEval, strings.ReplaceAll(validTask, "category: outcome", "category: boundary"))
	r := Inspect(path, Options{})
	require.Equal(t, Verified, r.Tasks[0].Requirements[0].State, "reference resolution only")
	require.True(t, hasDiagnostic(r, "boundary.post_run", Unresolved))
	require.False(t, r.Failed(false))
}

func TestInspectLocalRubricTriggerAndFilePaths(t *testing.T) {
	for _, tc := range []struct {
		name, kind, config, code string
	}{
		{"missing rubric", "prompt", "{rubric: './absent.md'}", "grader.rubric"},
		{"builtin rubric", "prompt", "{rubric: groundedness}", ""},
		{"missing trigger", "trigger", "{skill_path: './absent.md', mode: positive}", "grader.trigger_source"},
		{"blank code", "code", "{assertions: [' '], language: javascript}", "grader.configuration"},
		{"file content traversal", "file", "{content_patterns: [{path: '../outside', must_match: [ready]}]}", "grader.file_path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := "id: task\nname: Task\ninputs: {prompt: Inspect}\ngraders:\n  - name: local\n    type: " + tc.kind + "\n    config: " + tc.config + "\n"
			path, _ := writeSuite(t, validEval, task)
			r := Inspect(path, Options{})
			if tc.code == "" {
				require.False(t, r.Failed(false), "%+v", r.Diagnostics)
			} else {
				require.True(t, hasDiagnostic(r, tc.code, Invalid), "%+v", r.Diagnostics)
			}
		})
	}
}
