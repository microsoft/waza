package preflight

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectDirectoryFixtureAndSymlinkContainment(t *testing.T) {
	path, dir := writeSuite(t, validEval, strings.ReplaceAll(validTask, "  prompt:", "  context: {fixture: fixtures}\n  prompt:"))
	fixtures := filepath.Join(dir, "fixtures")
	require.NoError(t, os.Mkdir(fixtures, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "record.json"), []byte("{}"), 0600))
	require.False(t, Inspect(path, Options{}).Failed(false))
	if runtime.GOOS == "windows" {
		return
	}
	outside := filepath.Join(t.TempDir(), "record.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escaping.json")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"),
		[]byte(strings.ReplaceAll(validTask, "  prompt:", "  context: {fixture: escaping.json}\n  prompt:")), 0600))
	require.True(t, hasDiagnostic(Inspect(path, Options{}), "context.fixture", Invalid))
}

func TestInspectRepositorySourceRetainsRuntimeCwdBase(t *testing.T) {
	path, dir := writeSuite(t, validEval, strings.ReplaceAll(validTask, "  prompt:",
		"  repos: [{type: worktree, source: local-repo, dest: repo}]\n  prompt:"))
	cwd := t.TempDir()
	t.Chdir(cwd)
	require.NoError(t, os.Mkdir(filepath.Join(cwd, "local-repo"), 0700))
	r := Inspect(path, Options{})
	require.False(t, r.Failed(false), "%+v", r.Diagnostics)
	require.True(t, hasDiagnostic(r, "repository.revision", Unresolved))
	require.NoError(t, os.Remove(filepath.Join(cwd, "local-repo")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "local-repo"), 0700))
	require.True(t, hasDiagnostic(Inspect(path, Options{}), "repository.source", Invalid))
}

func TestInspectContextDirectoryResolutionAndTaskPrecedence(t *testing.T) {
	for _, mode := range []string{"default eval fixtures", "CLI relative to cwd", "task overrides CLI"} {
		t.Run(mode, func(t *testing.T) {
			task := strings.ReplaceAll(validTask, "  prompt:",
				"  files: [{path: record.json}]\n  prompt:")
			if mode == "task overrides CLI" {
				task += "\ncontext_dir: task-context\n"
			}
			path, dir := writeSuite(t, validEval, task)
			cwd := t.TempDir()
			t.Chdir(cwd)
			opts := Options{}
			expected := filepath.Join(dir, "fixtures")
			if mode != "default eval fixtures" {
				opts.ContextDir = "cli-context"
				expected = filepath.Join(cwd, opts.ContextDir)
			}
			if mode == "task overrides CLI" {
				expected = filepath.Join(cwd, "task-context")
			}
			require.NoError(t, os.Mkdir(expected, 0700))
			fixture := filepath.Join(expected, "record.json")
			require.NoError(t, os.WriteFile(fixture, []byte("{}"), 0600))
			r := Inspect(path, opts)
			require.False(t, r.Failed(true), "%+v", r.Diagnostics)
			require.Len(t, r.Tasks, 1)
			require.Equal(t, expected, r.Tasks[0].ContextDir)
			require.NoError(t, os.Remove(fixture))
			r = Inspect(path, opts)
			require.True(t, hasDiagnostic(r, "resource.file", Invalid), "%+v", r.Diagnostics)
			require.True(t, r.Failed(false))
		})
	}
}

func TestInspectRequiredSkillsUseEvalDeclarationDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, config, definition string
		wantInvalid              bool
	}{
		{"missing", "  required_skills: [example]\n  skill_directories: [skills]", "", true},
		{"present", "  required_skills: [example]\n  skill_directories: [skills]", "---\nname: example\ndescription: local\n---\nBody\n", false},
		{"no paths", "  required_skills: [example]", "", true},
		{"globally disabled", "  required_skills: [example]\n  skill_directories: [skills]\n  disabled_skills: ['*']", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, dir := writeSuite(t, strings.ReplaceAll(validEval, "  model: harness", "  model: harness\n"+tc.config), validTask)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "skills"), 0700))
			if tc.definition != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "SKILL.md"), []byte(tc.definition), 0600))
			}

			r := Inspect(path, Options{})
			require.Equal(t, tc.wantInvalid, hasDiagnostic(r, "skill.required", Invalid), "%+v", r.Diagnostics)
			require.Equal(t, tc.wantInvalid, r.Failed(false), "%+v", r.Diagnostics)
		})
	}
}

func TestInspectSnapshotUsesRuntimeContainment(t *testing.T) {
	path, dir := writeSuite(t, validEval, "id: task\nname: Task\ninputs: {prompt: Inspect}\ngraders:\n  - name: snapshot\n    type: diff\n    config:\n      context_dir: snapshots\n      expected_files: [{path: record.json, snapshot: '../outside.json'}]\n")
	t.Chdir(dir)
	require.NoError(t, os.Mkdir("snapshots", 0700))
	require.NoError(t, os.WriteFile("outside.json", []byte("{}"), 0600))
	require.True(t, hasDiagnostic(Inspect(path, Options{}), "grader.snapshot", Invalid))
}

func TestInspectFileResourceRuntimePathParity(t *testing.T) {
	for _, tc := range []struct {
		name, path, content string
		invalid             bool
	}{
		{"ordinary file", "record.json", "", false},
		{"double dot filename", "record..json", "", true},
		{"double dot directory", "records../record.json", "", true},
		{"parent segment remains rejected", "unused/../record.json", "", true},
		{"inline double dot filename", "record..json", "{}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaration := "  files: [{path: '" + tc.path + "'"
			if tc.content != "" {
				declaration += ", content: '" + tc.content + "'"
			}
			declaration += "}]\n  prompt:"
			path, dir := writeSuite(t, validEval, strings.ReplaceAll(validTask, "  prompt:", declaration))
			fixture := filepath.Join(dir, "fixtures", filepath.Clean(tc.path))
			require.NoError(t, os.MkdirAll(filepath.Dir(fixture), 0700))
			require.NoError(t, os.WriteFile(fixture, []byte("{}"), 0600))
			report := Inspect(path, Options{})
			require.Equal(t, tc.invalid, hasDiagnostic(report, "resource.path", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, tc.invalid, report.Failed(false), "%+v", report.Diagnostics)
		})
	}
}

func TestInspectExactScenarioVersionAndNoAmbientDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, version, context string
		valid, policy          bool
	}{
		{"scenario only", "2.0", "", true, false},
		{"scenario with explicit target", "2.0", "skill: example\n", true, true},
		{"v1 cannot select new semantics", "1.4", "", false, false},
		{"future minor unsupported", "2.1", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := strings.ReplaceAll(validEval, "skill: example\n", "scenario: local-workflow\n"+tc.context)
			eval = strings.ReplaceAll(eval, `schemaVersion: "1.4"`, `schemaVersion: "`+tc.version+`"`)
			path, _ := writeSuite(t, eval, validTask)
			cwd := t.TempDir()
			t.Chdir(cwd)
			require.NoError(t, os.WriteFile("example.agent.md", []byte("---\nname: example\ndescription: Ambient\ntools: []\n---\nBody\n"), 0600))
			r := Inspect(path, Options{})
			require.Equal(t, !tc.valid, r.Failed(false), "%+v", r.Diagnostics)
			require.Equal(t, tc.policy, hasDiagnostic(r, "boundary.requested_policy", Unresolved))
			if !tc.valid {
				require.False(t, r.Complete)
			}
		})
	}
}
