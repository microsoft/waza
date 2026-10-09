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
