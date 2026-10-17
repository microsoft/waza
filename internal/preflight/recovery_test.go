package preflight

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/models"
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

func TestRecoveryExternalMCPAggregatesIndependentMatcherFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, test := range []struct {
		name                      string
		external, invalid, legacy bool
	}{
		{"external then invalid regex", true, true, false},
		{"external then invalid legacy regex", true, true, true},
		{"external with valid regex", true, false, false},
		{"invalid regex only", false, true, false},
		{"self-contained valid matchers", false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			eval := recoveryEval("2.0") + "\nmcp_mocks:\n  - name: dependency\n    fixtures: mocks\n"
			path, dir := writeSuite(t, eval, validTask)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
			schema, pattern := `{}`, "^ready$"
			if test.external {
				schema = fmt.Sprintf(`{"$ref":%q}`, server.URL+"/schema.json")
			}
			if test.invalid {
				pattern = "["
			}
			output := `"sequence":[{"return":"ready"}]`
			if test.legacy {
				output = `"return":"ready"`
			}
			fixture := fmt.Sprintf(`{"responses":[
					{"match_schema":%s,"sequence":[{"return":null}]},
					{"match_regex":{"value":%q},%s}
				]}`, schema, pattern, output)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "read.json"), []byte(fixture), 0600))
			report := Inspect(path, Options{})
			require.Equal(t, test.invalid, report.Failed(false), "%+v", report.Diagnostics)
			require.Equal(t, test.invalid, hasDiagnostic(report, "mock.mcp", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, test.external && !test.invalid, hasDiagnostic(report, "mock.mcp", Unresolved), "%+v", report.Diagnostics)
			require.Equal(t, test.external && test.invalid, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
		})
	}
	require.Zero(t, requests.Load(), "offline validation must not fetch external prerequisites")
}

func TestRecoveryExternalMCPValidatesSequenceDespiteUnavailableMatcher(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, test := range []struct {
		name, sequence, invalid string
	}{
		{"empty", `[]`, "at least one step"},
		{"null", `null`, "not null"},
		{"wrong type", `{}`, "decoding fault sequence array"},
		{"too many steps", `[` + strings.Repeat(`{"return":null},`, 100) + `{"return":null}]`, "at most 100 steps"},
		{"excessive delay", `[{"return":null,"delay_ms":300001}]`, "between 0 and 300000"},
		{"negative delay", `[{"return":null,"delay_ms":-1}]`, "must be between 0"},
		{"invalid output", `[{"return":null,"error":"failed"}]`, "exactly one"},
		{"valid null output", `[{"return":null}]`, ""},
		{"maximum delay", `[{"return":null,"delay_ms":300000}]`, ""},
		{"maximum steps", `[` + strings.Repeat(`{"return":null},`, 99) + `{"return":null}]`, ""},
	} {
		for _, external := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/external=%t", test.name, external), func(t *testing.T) {
				eval := recoveryEval("2.0") + "\nmcp_mocks:\n  - name: dependency\n    fixtures: mocks\n"
				path, dir := writeSuite(t, eval, validTask)
				require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
				schema := `{}`
				if external {
					schema = fmt.Sprintf(`{"$ref":%q}`, server.URL+"/schema.json")
				}
				response := fmt.Sprintf(`{"match_schema":%s,"sequence":%s}`, schema, test.sequence)
				fixture := `{"responses":[` + response + `]}`
				require.NoError(t, os.WriteFile(filepath.Join(dir, "mocks", "read.json"), []byte(fixture), 0600))
				report := Inspect(path, Options{})
				invalid := test.invalid != ""
				require.Equal(t, invalid, report.Failed(false), "%+v", report.Diagnostics)
				require.Equal(t, invalid, hasDiagnostic(report, "mock.mcp", Invalid), "%+v", report.Diagnostics)
				require.Equal(t, external && !invalid, hasDiagnostic(report, "mock.mcp", Unresolved), "%+v", report.Diagnostics)
				require.Equal(t, external && invalid, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
				if invalid {
					require.ErrorContains(t, models.ValidateMCPFaultResponse([]byte(response), models.FaultSourceJSON, "2.0"), test.invalid)
				}
			})
		}
	}
	require.Zero(t, requests.Load(), "offline validation must not fetch external prerequisites")
}
