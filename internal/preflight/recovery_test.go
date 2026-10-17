package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func recoveryEval(version string) string {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
	if version == "2.0" {
		return strings.ReplaceAll(eval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: recovery")
	}
	return eval
}

func TestRecoveryTaskAdmissionUsesEnclosingVersion(t *testing.T) {
	task := validTask + "\ncommand_mocks:\n  - name: az\n    responses:\n      - args: []\n        sequence: [{stdout: ready}]\n"
	for _, version := range []string{"1.4", "2.0"} {
		t.Run(version, func(t *testing.T) {
			path, _ := writeSuite(t, recoveryEval(version), task)
			report := Inspect(path, Options{})
			require.Equal(t, version != "2.0", hasDiagnostic(report, "task.version", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, version != "2.0", report.Failed(false), "%+v", report.Diagnostics)
		})
	}
}

func TestRecoveryCommandFixturesUseEffectiveMocks(t *testing.T) {
	eval := recoveryEval("2.0") + "\ncommand_mocks:\n  - name: az\n    responses:\n      - args: []\n        sequence: [{fixture: response.txt}]\n"
	for _, override := range []string{"", "\ncommand_mocks: []\n"} {
		t.Run("override="+override, func(t *testing.T) {
			path, dir := writeSuite(t, eval, validTask+override)
			report := Inspect(path, Options{})
			require.Equal(t, override == "", hasDiagnostic(report, "mock.command_fixture", Invalid))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "response.txt"), []byte("ready"), 0600))
			report = Inspect(path, Options{})
			require.False(t, report.Failed(false), "%+v", report.Diagnostics)
		})
	}
}

func TestRecoveryExternalMCPAdmissionAndInlineReplacement(t *testing.T) {
	for _, test := range []struct {
		name, version, response, inline string
		invalid                         bool
	}{
		{"empty sequence", "2.0", `{"sequence":[]}`, "", true},
		{"null sequence", "2.0", `{"sequence":null}`, "", true},
		{"wrong version", "1.4", `{"sequence":[{"return":"ready"}]}`, "", true},
		{"valid scenario", "2.0", `{"sequence":[{"return":"ready"}]}`, "", false},
		{"legacy response", "1.4", `{"return":"ready"}`, "", false},
		{"overridden sequence", "1.4", `{"sequence":[]}`, "\n    tools:\n      read:\n        responses: [{return: ready}]", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			eval := recoveryEval(test.version) + "\nmcp_mocks:\n  - name: dependency\n    fixtures: mocks" + test.inline + "\n"
			path, dir := writeSuite(t, eval, validTask)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "read.json"), []byte(`{"responses":[`+test.response+`]}`), 0600))
			report := Inspect(path, Options{})
			require.Equal(t, test.invalid, hasDiagnostic(report, "mock.mcp", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, test.invalid, report.Failed(false), "%+v", report.Diagnostics)
		})
	}
}
