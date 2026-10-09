package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectRuntimeWorkspaceDefaultsSatisfyLegacyRequiredSkills(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("paths:\n  skills: custom-skills\n"), 0600))
	dir := filepath.Join(cwd, "custom-skills", "example")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: example\ndescription: Local skill\n---\nBody\n"), 0600))
	eval := strings.ReplaceAll(validEval, "  model: harness", "  model: harness\n  required_skills: [example]")
	path, _ := writeSuite(t, eval, validTask)
	r := Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.False(t, hasDiagnostic(r, "skill.required", Invalid))
}

func TestInspectWorkspaceDefaultsDoNotEnableScenarioAmbientSkills(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("malformed: ["), 0600))
	eval := strings.ReplaceAll(strings.ReplaceAll(validEval, `schemaVersion: "1.4"`, `schemaVersion: "2.0"`), "skill: example", "scenario: local-workflow")
	path, _ := writeSuite(t, eval, validTask)
	r := Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.False(t, hasDiagnostic(r, "workspace.configuration", Unresolved), "disabled discovery does not inspect ambient configuration")
	require.True(t, hasDiagnostic(r, "skills.discovery_disabled", Verified))
}

func TestInspectWorkspaceConfigurationDiagnosticsArePayloadFree(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("defaults: {workers: secret-payload}"), 0600))
	path, _ := writeSuite(t, validEval, validTask)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "workspace.configuration", Unresolved))
	require.False(t, r.Complete)
	for _, d := range r.Diagnostics {
		require.NotContains(t, d.Message, "secret-payload")
		require.NotContains(t, d.Remediation, "secret-payload")
	}
}
