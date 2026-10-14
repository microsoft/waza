package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInspectWorkspaceBlankTargetParity(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	workspace := []byte("paths:\n  skills: custom-skills\n")
	require.NoError(t, os.WriteFile(".waza.yaml", workspace, 0o600))
	skillDir := filepath.Join(cwd, "custom-skills", "example")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: example\ndescription: Local skill\n---\nBody\n"), 0o600))
	canonicalSkillDir, err := filepath.EvalSymlinks(skillDir)
	require.NoError(t, err)
	for _, target := range []struct {
		name, field, want string
		blank             bool
	}{
		{"absent", "", "", true},
		{"null", "skill: null\n", "", true},
		{"empty", "skill: \"\"\n", "", true},
		{"whitespace", "skill: \" \\t \"\n", " \t ", true},
		{"nonblank", "skill: example\n", "example", false},
		{"padded nonblank", "skill: ' example '\n", " example ", false},
	} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", target.name, explicit), func(t *testing.T) {
				eval := strings.Replace(validEval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: local-workflow", 1)
				eval = strings.Replace(eval, "skill: example\n", target.field, 1)
				if explicit {
					eval = strings.Replace(eval, "  model: harness", "  model: harness\n  skill_directories: [' explicit ']", 1)
				}
				spec, err := models.ParseEvalSpecOffline([]byte(eval), "eval.yaml", nil)
				require.NoError(t, err)
				report := &Report{Complete: true}
				applyWorkspaceSkillDefaults(report, spec)
				require.Equal(t, target.want, spec.SkillName, "a blank-target check must not rewrite nonblank or whitespace values")
				switch {
				case explicit:
					require.Equal(t, []string{" explicit "}, spec.Config.SkillPaths, "explicit directories retain their original spelling")
					require.False(t, spec.SkillsDisabledForTask(nil))
				case target.blank:
					require.Empty(t, spec.Config.SkillPaths, "scenario-only runs must not acquire ambient workspace directories")
					require.True(t, spec.SkillsDisabledForTask(nil))
					path, _ := writeSuite(t, eval, validTask)
					full := Inspect(path, Options{})
					require.True(t, hasDiagnostic(full, "skills.discovery_disabled", Verified), "%+v", full.Diagnostics)
					schemaInvalid := target.name == "null" || target.name == "empty"
					require.Equal(t, schemaInvalid, full.Failed(true), "%+v", full.Diagnostics)
					require.Equal(t, schemaInvalid, hasDiagnostic(full, "eval.schema", Invalid))
				default:
					require.Contains(t, spec.Config.SkillPaths, canonicalSkillDir, "a configured nonblank target retains runtime workspace discovery")
					require.False(t, spec.SkillsDisabledForTask(nil))
				}
				require.Empty(t, report.Diagnostics)
			})
		}
	}
	for _, header := range []string{"", "scenario: null\n", "scenario: \"\"\n"} {
		t.Run("header/"+header, func(t *testing.T) {
			eval := strings.Replace(validEval, "name: offline-example", header+"name: offline-example", 1)
			if header != "" {
				eval = strings.Replace(eval, `schemaVersion: "1.4"`, `schemaVersion: "2.0"`, 1)
			}
			spec, err := models.ParseEvalSpecOffline([]byte(eval), "eval.yaml", nil)
			if header != "" {
				require.Error(t, err, "null/empty scenario headers must not be reinterpreted as scenario-only runs")
				return
			}
			require.NoError(t, err)
			applyWorkspaceSkillDefaults(&Report{Complete: true}, spec)
			require.Contains(t, spec.Config.SkillPaths, canonicalSkillDir, "absent scenario retains legacy workspace discovery")
		})
	}
	after, err := os.ReadFile(".waza.yaml")
	require.NoError(t, err)
	require.Equal(t, workspace, after)
}
