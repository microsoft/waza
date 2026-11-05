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

	"github.com/microsoft/waza/internal/mcpmock"
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

func TestRecoveryTaskVersionSurvivesUnavailableGraderSchemas(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, scope := range []string{"task", "checkpoint"} {
		for _, version := range []string{"1.4", "2.0"} {
			for _, external := range []bool{false, true} {
				for _, sequence := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/version=%s/external=%t/sequence=%t", scope, version, external, sequence), func(t *testing.T) {
						schema := map[string]any{"type": "object"}
						if external {
							schema = map[string]any{"$ref": server.URL + "/schema.json"}
						}
						grader := offlineMatcherDeclaration("schema", map[string]any{"json_schema": schema})
						task := "id: task\nname: Task\ninputs: {prompt: Inspect.}\n"
						if scope == "task" {
							task += pipelineYAML(t, map[string]any{"graders": []any{grader}})
						} else {
							task += pipelineYAML(t, map[string]any{"checkpoints": []any{map[string]any{"after_turn": 1, "graders": []any{grader}}}})
						}
						output := "stdout: ready"
						if sequence {
							output = "sequence: [{stdout: ready}]"
						}
						task += "\ncommand_mocks:\n  - name: az\n    responses:\n      - args: []\n        " + output + "\n"
						eval := recoveryEval(version)
						path, dir := writeSuite(t, eval, task)
						report := Inspect(path, Options{})
						invalid := sequence && version != "2.0"
						require.False(t, hasDiagnostic(report, "eval.schema", Invalid), "%+v", report.Diagnostics)
						require.False(t, hasDiagnostic(report, "task.schema", Invalid), "%+v", report.Diagnostics)
						require.Equal(t, invalid, report.Failed(false), "%+v", report.Diagnostics)
						require.Equal(t, invalid && !external, hasDiagnostic(report, "task.version", Invalid), "%+v", report.Diagnostics)
						require.Equal(t, invalid && external, hasDiagnostic(report, "task.configuration", Invalid), "%+v", report.Diagnostics)
						require.Equal(t, external && !invalid, hasDiagnostic(report, "task.configuration", Unresolved), "%+v", report.Diagnostics)
						require.Equal(t, external && invalid, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
						require.Equal(t, !external, report.Complete)
						after, err := os.ReadFile(path)
						require.NoError(t, err)
						require.Equal(t, eval, string(after))
						after, err = os.ReadFile(filepath.Join(dir, "task.yaml"))
						require.NoError(t, err)
						require.Equal(t, task, string(after))
					})
				}
			}
		}
	}
	require.Zero(t, requests.Load(), "version eligibility checks must not fetch unavailable grader schemas")
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

func TestRecoveryExternalMCPResponseDelayAdmission(t *testing.T) {
	for _, version := range []string{"1.4", "2.0"} {
		for _, bundled := range []bool{false, true} {
			for _, test := range []struct {
				name, response    string
				override, invalid bool
			}{
				{"response delay", `{"return":"ready","delay_ms":300000}`, false, true},
				{"zero delay", `{"return":"ready","delay_ms":0}`, false, true},
				{"null delay", `{"return":"ready","delay_ms":null}`, false, true},
				{"string delay", `{"return":"ready","delay_ms":"slow"}`, false, true},
				{"boolean delay", `{"return":"ready","delay_ms":false}`, false, true},
				{"legacy response", `{"return":"ready"}`, false, false},
				{"zero step delay", `{"sequence":[{"return":"ready","delay_ms":0}]}`, false, version != "2.0"},
				{"maximum step delay", `{"sequence":[{"return":"ready","delay_ms":300000}]}`, false, version != "2.0"},
				{"overridden response delay", `{"return":"unused","delay_ms":300000}`, true, false},
				{"overridden null delay", `{"return":"unused","delay_ms":null}`, true, false},
			} {
				t.Run(fmt.Sprintf("%s/version=%s/bundle=%t", test.name, version, bundled), func(t *testing.T) {
					mock := models.MCPMockConfig{Name: "dependency", Fixtures: "mocks"}
					eval := recoveryEval(version) + "\nmcp_mocks:\n  - name: dependency\n    fixtures: mocks\n"
					if test.override {
						mock.Tools = map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{{Return: "inline ready"}}}}
						eval += "    tools:\n      read:\n        responses: [{return: inline ready}]\n"
					}
					path, dir := writeSuite(t, eval, validTask)
					require.NoError(t, os.Mkdir(filepath.Join(dir, "mocks"), 0700))
					fixture := `{"responses":[` + test.response + `]}`
					if bundled {
						fixture = `{"tools":{"read":` + fixture + `}}`
					}
					fixturePath := filepath.Join(dir, "mocks", "read.json")
					require.NoError(t, os.WriteFile(fixturePath, []byte(fixture), 0600))
					before, err := os.ReadFile(fixturePath)
					require.NoError(t, err)
					require.Equal(t, fixture, string(before), "the actual external source must contain the tested declaration")

					report := Inspect(path, Options{})
					require.Equal(t, test.invalid, report.Failed(false), "%+v", report.Diagnostics)
					require.Equal(t, test.invalid, hasDiagnostic(report, "mock.mcp", Invalid), "%+v", report.Diagnostics)
					state := Verified
					if test.invalid {
						state = Invalid
						require.Contains(t, report.Diagnostics, Diagnostic{
							Code: "mock.mcp", State: Invalid, Severity: "error", Source: path,
							Message:     "MCP mock fixtures or matching configuration could not be verified.",
							Remediation: "Check fixture files, tool responses, regexes, and self-contained match_schema references.",
						})
					}
					require.Contains(t, report.Dependencies, Dependency{Kind: "mcp", Name: "dependency", Mode: "mocked", State: state})
					for _, resolve := range []func(models.MCPMockConfig, string, string) (*mcpmock.Config, error){
						mcpmock.FromEvalConfigWithVersion, mcpmock.FromEvalConfigOfflineWithVersion,
					} {
						cfg, err := resolve(mock, dir, version)
						if test.invalid {
							message := "delay_ms is only supported inside a finite sequence step"
							if strings.Contains(test.response, `"sequence"`) {
								message = "fault sequence requires explicit enclosing scenario schemaVersion 2.0"
							}
							require.EqualError(t, err, `mcp mock "dependency" fixtures: tool "read": response 0: `+message)
						} else {
							require.NoError(t, err)
							require.Len(t, cfg.Tools["read"].Responses, 1)
							response := cfg.Tools["read"].Responses[0]
							if test.override {
								require.Equal(t, "inline ready", response.Return)
								require.Empty(t, response.Sequence, "override replaces the whole tool")
							} else if strings.Contains(test.response, `"sequence"`) {
								require.Len(t, response.Sequence, 1)
								require.Equal(t, "ready", response.Sequence[0].Return)
							} else {
								require.Equal(t, "ready", response.Return)
							}
						}
					}
					after, err := os.ReadFile(fixturePath)
					require.NoError(t, err)
					require.Equal(t, before, after)
					after, err = os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, eval, string(after))
				})
			}
		}
	}
}

func TestRecoveryInlineMCPAggregatesIndependentMatcherFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, test := range []struct {
		name                               string
		external, invalid, legacy, reverse bool
	}{
		{"external then invalid finite regex", true, true, false, false},
		{"external then invalid legacy regex", true, true, true, false},
		{"invalid finite regex then external", true, true, false, true},
		{"invalid legacy regex then external", true, true, true, true},
		{"external with valid regex", true, false, false, false},
		{"invalid regex only", false, true, false, false},
		{"self-contained valid matchers", false, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, pattern := `{}`, "^ready$"
			if test.external {
				schema = fmt.Sprintf(`{"$ref":%q}`, server.URL+"/schema.json")
			}
			if test.invalid {
				pattern = "["
			}
			first := fmt.Sprintf(`{"match_schema":%s,"sequence":[{"return":null}]}`, schema)
			output := `"sequence":[{"return":"ready"}]`
			if test.legacy {
				output = `"return":"ready"`
			}
			second := fmt.Sprintf(`{"match_regex":{"operation":%q},%s}`, pattern, output)
			if test.reverse {
				first, second = second, first
			}
			eval := recoveryEval("2.0") + fmt.Sprintf("\nmcp_mocks:\n  - name: dependency\n    tools:\n      read:\n        responses: [%s,%s]\n", first, second)
			path, _ := writeSuite(t, eval, validTask)
			sources, err := models.MockResponseSources([]byte(eval))
			require.NoError(t, err)
			require.Len(t, sources, 2, "exercise inline declarations, not external fixture admission")
			report := Inspect(path, Options{})
			require.False(t, hasDiagnostic(report, "eval.schema", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, test.invalid, report.Failed(false), "%+v", report.Diagnostics)
			if test.external {
				require.True(t, report.Failed(true), "%+v", report.Diagnostics)
			}
			require.Equal(t, test.invalid, hasDiagnostic(report, "eval.configuration", Invalid), "%+v", report.Diagnostics)
			require.Equal(t, test.external && !test.invalid, hasDiagnostic(report, "eval.configuration", Unresolved), "%+v", report.Diagnostics)
			require.Equal(t, test.external && test.invalid, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
			require.Equal(t, !test.external && !test.invalid, report.Complete)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, eval, string(after))
		})
	}
	require.Zero(t, requests.Load(), "offline inline admission must not fetch unavailable matcher schemas")
}

func TestRecoveryFaultSourcesRetainIndependentOfflineGraderDiagnostics(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	for _, scope := range []string{"eval", "task", "checkpoint"} {
		t.Run(scope, func(t *testing.T) {
			eval, task, code := recoveryEval("2.0"), "id: task\nname: Task\ninputs: {prompt: Inspect.}\n", "eval.configuration"
			external := offlineMatcherDeclaration("external", map[string]any{"json_schema": map[string]any{"$ref": server.URL}})
			switch scope {
			case "eval":
				eval += fmt.Sprintf("\nmcp_mocks:\n  - name: dependency\n    tools:\n      read:\n        responses: [{match_schema: {$ref: %q}, sequence: [{return: null}]}]\n", server.URL)
				eval += "\ngraders:\n  - name: invalid\n    type: text\n    config: {regex_match: ['[']}\n"
			case "task":
				task += pipelineYAML(t, map[string]any{"graders": []any{external}})
				code = "task.configuration"
			case "checkpoint":
				task += pipelineYAML(t, map[string]any{"checkpoints": []any{map[string]any{"after_turn": 1, "graders": []any{external}}}})
				code = "task.configuration"
			}
			if scope != "eval" {
				task += "\ncommand_mocks:\n  - name: az\n    responses:\n      - args_regex: ['[']\n        sequence: [{stdout: ready}]\n"
			}
			path, dir := writeSuite(t, eval, task)
			report := Inspect(path, Options{})
			require.False(t, hasDiagnostic(report, "eval.schema", Invalid), "%+v", report.Diagnostics)
			require.False(t, hasDiagnostic(report, "task.schema", Invalid), "%+v", report.Diagnostics)
			require.True(t, report.Failed(false), "%+v", report.Diagnostics)
			require.False(t, report.Complete)
			require.True(t, hasDiagnostic(report, code, Invalid), "%+v", report.Diagnostics)
			require.True(t, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, eval, string(after))
			after, err = os.ReadFile(filepath.Join(dir, "task.yaml"))
			require.NoError(t, err)
			require.Equal(t, task, string(after))
		})
	}
	require.Zero(t, requests.Load(), "independent diagnostic aggregation must remain offline")
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
