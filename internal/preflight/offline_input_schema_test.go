package preflight

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
)

func TestInspectOfflineInputSchemaAfterResponseError(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, source := range []string{"fixture", "inline"} {
		for _, ref := range refs {
			for _, scenario := range []bool{false, true} {
				for _, schema := range []struct {
					name    string
					value   any
					invalid bool
				}{
					{"string", "object", true},
					{"number", 42, true},
					{"boolean", true, true},
					{"array", []any{"object"}, true},
					{"invalid object", map[string]any{"type": 42}, true},
					{"valid object", map[string]any{"type": "object"}, false},
					{"local fragment", map[string]any{"$defs": map[string]any{"value": map[string]any{"type": "object"}}, "$ref": "#/$defs/value"}, false},
					{"local filename", map[string]any{"$defs": map[string]any{"value": map[string]any{"type": "object"}}, "$ref": "mock-input-schema.json#/$defs/value"}, false},
					{"absent", nil, false},
				} {
					for _, invalidRegex := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/scenario=%t/%s/regex=%t", source, ref, scenario, schema.name, invalidRegex), func(t *testing.T) {
							response := map[string]any{"match_schema": map[string]any{"$ref": ref}, "return": map[string]any{"ready": true}}
							if invalidRegex {
								response["match_regex"] = map[string]any{"value": "["}
							}
							tool := map[string]any{"input_schema": schema.value, "responses": []any{response}}
							if schema.value == nil {
								delete(tool, "input_schema")
							}
							mock := map[string]any{"name": "input"}
							if source == "fixture" {
								mock["fixtures"] = "mock-fixtures"
							} else {
								mock["tools"] = map[string]any{"read": tool}
							}
							eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
							if scenario {
								eval = strings.Replace(eval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: offline-input", 1)
								eval = strings.Replace(eval, "skill: example\n", "", 1)
							}
							eval += pipelineYAML(t, map[string]any{"mcp_mocks": []any{mock}})
							path, dir := writeSuite(t, eval, validTask)
							var fixtureData []byte
							var fixturePath string
							if source == "fixture" {
								fixtures := filepath.Join(dir, "mock-fixtures")
								require.NoError(t, os.Mkdir(fixtures, 0o700))
								var err error
								fixtureData, err = json.Marshal(tool)
								require.NoError(t, err)
								fixturePath = filepath.Join(fixtures, "read.json")
								require.NoError(t, os.WriteFile(fixturePath, fixtureData, 0o600))
							}
							report := Inspect(path, Options{})
							require.Equal(t, schema.invalid || invalidRegex, report.Failed(false), "%+v", report.Diagnostics)
							require.True(t, report.Failed(true))
							require.False(t, report.Complete)
							if source == "fixture" {
								if schema.invalid {
									require.True(t, hasDiagnostic(report, "mock.input_schema", Invalid), "%+v", report.Diagnostics)
								}
								state := Unresolved
								if schema.invalid || invalidRegex {
									state = Invalid
								}
								require.Equal(t, state, report.Dependencies[0].State)
								cfg, err := mcpmock.FromEvalConfigOffline(models.MCPMockConfig{Name: "input", Fixtures: "mock-fixtures"}, dir)
								require.Error(t, err)
								require.NotNil(t, cfg, "resolved declarations must survive response validation failures")
								validationErr := err
								expectedInput, err := json.Marshal(schema.value)
								require.NoError(t, err)
								actualInput, err := json.Marshal(cfg.Tools["read"].InputSchema)
								require.NoError(t, err)
								require.JSONEq(t, string(expectedInput), string(actualInput))
								_, external := schemaloader.ClassifyError(validationErr)
								require.True(t, external)
								if invalidRegex {
									cfg, err = mcpmock.FromEvalConfig(models.MCPMockConfig{Name: "input", Fixtures: "mock-fixtures"}, dir)
									require.Error(t, err)
									require.Nil(t, cfg, "runtime retains fail-fast response validation")
								}
								after, err := os.ReadFile(fixturePath)
								require.NoError(t, err)
								require.Equal(t, fixtureData, after)
							} else {
								_, err := models.LoadEvalSpecOffline(path)
								require.Error(t, err)
								state := Unresolved
								if schema.invalid || invalidRegex {
									state = Invalid
								}
								require.Equal(t, state, schemaState(err), "native offline guard must retain invalid input schemas")
								require.True(t, hasDiagnostic(report, "eval.configuration", state), "%+v", report.Diagnostics)
								if state == Invalid {
									require.True(t, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
								}
							}
							after, err := os.ReadFile(path)
							require.NoError(t, err)
							require.Equal(t, eval, string(after))
							require.Zero(t, requests.Load(), "input and response validation must never fetch schemas")
						})
					}
				}
			}
		}
	}
}
