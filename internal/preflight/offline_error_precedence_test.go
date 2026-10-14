package preflight

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestInspectOfflineErrorPrecedence(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	externalFile := filepath.Join(t.TempDir(), "unread-schema.json")
	require.NoError(t, os.WriteFile(externalFile, []byte("not JSON"), 0o600))
	for _, target := range []string{server.URL, testutil.FileURL(externalFile)} {
		for _, scope := range []string{"eval", "task", "checkpoint"} {
			for _, reverse := range []bool{false, true} {
				for _, problem := range []string{"text regex", "schema", "static schema", "matcher regex", "same declaration", "valid", "external only", "invalid only"} {
					t.Run(fmt.Sprintf("%s/%s/reverse=%t/%s", target, scope, reverse, problem), func(t *testing.T) {
						external := offlineMatcherDeclaration("external", map[string]any{"json_schema": map[string]any{"$ref": target}})
						invalid := map[string]any{"name": "independent", "type": "text", "config": map[string]any{"regex_match": []string{"["}}}
						switch problem {
						case "schema":
							invalid = offlineMatcherDeclaration("invalid", map[string]any{"json_schema": map[string]any{"type": "not-a-type"}})
						case "static schema":
							invalid = map[string]any{"name": "invalid", "type": "json_schema", "config": map[string]any{"schema": map[string]any{"type": "not-a-type"}}}
						case "matcher regex", "same declaration":
							invalid = offlineMatcherDeclaration("invalid", map[string]any{"regex": "["})
						case "valid":
							invalid = map[string]any{"name": "valid", "type": "text", "config": map[string]any{"contains": []string{"ready"}}}
						}
						declarations := []any{external, invalid}
						if reverse {
							declarations[0], declarations[1] = declarations[1], declarations[0]
						}
						switch problem {
						case "same declaration":
							expectations := []any{
								map[string]any{"tool": "read", "args": map[string]any{"value": map[string]any{"json_schema": map[string]any{"$ref": target}}}},
								map[string]any{"tool": "read", "args": map[string]any{"value": map[string]any{"regex": "["}}},
							}
							if reverse {
								expectations[0], expectations[1] = expectations[1], expectations[0]
							}
							declarations = []any{map[string]any{"name": "mixed", "type": "tool_calls", "config": map[string]any{"expect": expectations}}}
						case "external only":
							declarations = []any{external}
						case "invalid only":
							declarations = []any{invalid}
						}
						eval, task, code := validEval, "id: task\nname: Task\ninputs: {prompt: Inspect.}\n", "eval.configuration"
						switch scope {
						case "eval":
							eval += pipelineYAML(t, map[string]any{"graders": declarations})
						case "task":
							task += pipelineYAML(t, map[string]any{"graders": declarations})
							code = "task.configuration"
						case "checkpoint":
							task += pipelineYAML(t, map[string]any{"checkpoints": []any{map[string]any{"after_turn": 1, "graders": declarations}}})
							code = "task.configuration"
						}
						path, dir := writeSuite(t, eval, task)
						report := Inspect(path, Options{})
						wantInvalid := problem != "valid" && problem != "external only"
						require.Equal(t, wantInvalid, report.Failed(false), "%+v", report.Diagnostics)
						require.True(t, report.Failed(true))
						if problem != "invalid only" {
							require.False(t, report.Complete, "external dependencies must prevent a complete inventory")
							require.True(t, hasOfflineDiagnostic(report), "%+v", report.Diagnostics)
						}
						if wantInvalid && problem != "invalid only" {
							require.True(t, hasDiagnostic(report, code, Invalid), "%+v", report.Diagnostics)
						}
						after, err := os.ReadFile(path)
						require.NoError(t, err)
						require.Equal(t, eval, string(after))
						after, err = os.ReadFile(filepath.Join(dir, "task.yaml"))
						require.NoError(t, err)
						require.Equal(t, task, string(after))
						require.Zero(t, requests.Load(), "offline guard and static validation must not fetch schemas")
					})
				}
			}
			after, err := os.ReadFile(externalFile)
			require.NoError(t, err)
			require.Equal(t, "not JSON", string(after))
		}
	}
}

func offlineMatcherDeclaration(name string, matcher map[string]any) map[string]any {
	return map[string]any{"name": name, "type": "tool_calls",
		"config": map[string]any{"expect": []any{map[string]any{"tool": "read", "args": map[string]any{"value": matcher}}}}}
}

func hasOfflineDiagnostic(report *Report) bool {
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.State == Unresolved && (strings.Contains(diagnostic.Code, "configuration") || strings.Contains(diagnostic.Code, "external")) {
			return true
		}
	}
	return false
}

func TestOfflineModelGuardAggregatesMatcherErrors(t *testing.T) {
	for _, declaration := range []struct{ kind, field string }{
		{"tool_calls", "expect"},
		{"tool_constraint", "expect_tools"},
		{"tool_constraint", "reject_tools"},
		{"tool_constraint", "allow_only"},
	} {
		for _, reverse := range []bool{false, true} {
			for _, invalid := range []map[string]any{{"regex": "["}, {"json_schema": map[string]any{"type": "invalid"}}} {
				t.Run(fmt.Sprintf("%s/%s/reverse=%t/%v", declaration.kind, declaration.field, reverse, invalid), func(t *testing.T) {
					expectations := []any{
						map[string]any{"tool": "read", "args": map[string]any{"value": map[string]any{"json_schema": map[string]any{"$ref": "https://never-read.invalid/schema"}}}},
						map[string]any{"tool": "read", "args": map[string]any{"value": invalid}},
					}
					if reverse {
						expectations[0], expectations[1] = expectations[1], expectations[0]
					}
					data := []byte(pipelineYAML(t, map[string]any{"name": "mixed", "type": declaration.kind, "config": map[string]any{declaration.field: expectations}}))
					original := append([]byte(nil), data...)
					_, err := models.DecodeGraderConfigOffline(data)
					require.Error(t, err)
					require.Equal(t, Invalid, schemaState(err), "an external reference must not hide an independent invalid matcher")
					require.Equal(t, original, data)
				})
			}
		}
	}
}

func TestOfflineMatcherAliasPreservesPolicy(t *testing.T) {
	for _, schema := range []struct {
		name, value string
		state       State
	}{
		{"local", "{type: object}", Verified},
		{"external", "{$ref: 'https://never-read.invalid/schema'}", Unresolved},
	} {
		t.Run(schema.name, func(t *testing.T) {
			data := []byte(`name: aliases
type: tool_calls
config:
  expect:
    - tool: read
      args:
        first: &shared
          json_schema: ` + schema.value + `
        second: *shared
`)
			original := append([]byte(nil), data...)
			_, err := models.DecodeGraderConfigOffline(data)
			if schema.state == Verified {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, schema.state, schemaState(err))
			}
			require.Equal(t, original, data)
		})
	}
}

func TestInspectOfflineMCPResponseErrorPrecedence(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			responses := []any{
				map[string]any{"match_schema": map[string]any{"$ref": "https://never-read.invalid/schema"}, "return": "ready"},
				map[string]any{"match_regex": map[string]any{"value": "["}, "return": "ready"},
			}
			if reverse {
				responses[0], responses[1] = responses[1], responses[0]
			}
			eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") +
				pipelineYAML(t, map[string]any{"mcp_mocks": []any{map[string]any{"name": "mock", "tools": map[string]any{"read": map[string]any{"responses": responses}}}}})
			path, _ := writeSuite(t, eval, validTask)
			report := Inspect(path, Options{})
			require.True(t, report.Failed(false), "%+v", report.Diagnostics)
			require.False(t, report.Complete)
			require.True(t, hasDiagnostic(report, "eval.configuration", Invalid), "%+v", report.Diagnostics)
			require.True(t, hasOfflineDiagnostic(report), "%+v", report.Diagnostics)
		})
	}
	t.Run("same response", func(t *testing.T) {
		response := map[string]any{"match_schema": map[string]any{"$ref": "https://never-read.invalid/schema"},
			"match_regex": map[string]any{"value": "["}, "return": "ready"}
		eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") +
			pipelineYAML(t, map[string]any{"mcp_mocks": []any{map[string]any{"name": "mock", "tools": map[string]any{"read": map[string]any{"responses": []any{response}}}}}})
		path, _ := writeSuite(t, eval, validTask)
		report := Inspect(path, Options{})
		require.True(t, report.Failed(false), "%+v", report.Diagnostics)
		require.False(t, report.Complete)
		require.True(t, hasDiagnostic(report, "eval.configuration", Invalid), "%+v", report.Diagnostics)
		require.True(t, hasOfflineDiagnostic(report), "%+v", report.Diagnostics)
	})
}

func TestSchemaStateJoinedInvalidTakesPrecedence(t *testing.T) {
	external := &jsonschema.LoadURLError{URL: "https://never-read.invalid/schema", Err: schemaloader.ErrExternalReference}
	invalid := errors.New("invalid regex")
	for _, err := range []error{
		errors.Join(external, invalid),
		errors.Join(invalid, external),
		fmt.Errorf("offline guard: %w", errors.Join(external, invalid)),
		errors.Join(external, errors.Join(schemaloader.ErrExternalReference, invalid)),
	} {
		require.Equal(t, Invalid, schemaState(err))
	}
	require.Equal(t, Unresolved, schemaState(errors.Join(external, schemaloader.ErrExternalReference)))
}
