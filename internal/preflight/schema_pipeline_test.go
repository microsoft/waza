package preflight

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInspectArgumentSchemaPipeline(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	external := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(external, []byte(`{"type":"object"}`), 0600))
	fileRef := testutil.FileURL(external)
	for _, constraint := range []struct{ kind, field string }{
		{"tool_calls", "expect"},
		{"tool_constraint", "expect_tools"},
		{"tool_constraint", "reject_tools"},
		{"tool_constraint", "allow_only"},
	} {
		for _, source := range []string{"eval", "task", "checkpoint", "cache", "merge"} {
			for _, schema := range []struct {
				name    string
				matcher map[string]any
				state   State
			}{
				{"file", map[string]any{"json_schema": map[string]any{"$ref": fileRef}}, Unresolved},
				{"HTTP", map[string]any{"json_schema": map[string]any{"$ref": server.URL}}, Unresolved},
				{"external metaschema", map[string]any{"json_schema": map[string]any{"$schema": server.URL, "type": "object"}}, Unresolved},
				{"fragment", map[string]any{"json_schema": map[string]any{"$defs": map[string]any{"value": map[string]any{"type": "object"}}, "$ref": "#/$defs/value"}}, Verified},
				{"payload", map[string]any{"equals": map[string]any{"json_schema": map[string]any{"$ref": fileRef}}}, Verified},
				{"multiple kinds", map[string]any{"equals": 1, "json_schema": map[string]any{"$ref": fileRef}}, Invalid},
				{"unknown kind", map[string]any{"unknown": map[string]any{"$ref": fileRef}}, Invalid},
			} {
				t.Run(constraint.kind+"/"+constraint.field+"/"+source+"/"+schema.name, func(t *testing.T) {
					declaration := map[string]any{"name": "check", "type": constraint.kind,
						"config": map[string]any{constraint.field: []any{map[string]any{"tool": "read", "args": map[string]any{"value": schema.matcher}}}}}
					eval, task := validEval, validTask
					code := "eval.configuration"
					switch source {
					case "eval":
						eval += pipelineYAML(t, map[string]any{"graders": []any{declaration}})
					case "task":
						task = "id: task\nname: Task\ninputs: {prompt: Inspect.}\n" + pipelineYAML(t, map[string]any{"graders": []any{declaration}})
						code = "task.configuration"
					case "checkpoint":
						task += pipelineYAML(t, map[string]any{"checkpoints": []any{map[string]any{"after_turn": 1, "graders": []any{declaration}}}})
						code = "task.configuration"
					}
					path, dir := writeSuite(t, eval, task)
					if source == "cache" || source == "merge" {
						code = "grader.cache_integrity"
						const ref = "github.com/example/checks/check.yaml@v1"
						override := map[string]any{"name": "locked", "ref": ref}
						preset := declaration
						if source == "merge" {
							code = "grader.merge"
							override["config"] = declaration["config"]
							preset = map[string]any{"name": "check", "type": constraint.kind,
								"config": map[string]any{constraint.field: []any{map[string]any{"tool": "read"}}}}
						}
						require.NoError(t, os.WriteFile(path, []byte(validEval+pipelineYAML(t, map[string]any{"graders": []any{override}})), 0600))
						cache := t.TempDir()
						t.Setenv("WAZA_MODULE_CACHE", cache)
						commit := strings.Repeat("a", 40)
						module := filepath.Join(cache, "github.com", "example", "checks", commit)
						require.NoError(t, os.MkdirAll(module, 0700))
						require.NoError(t, os.WriteFile(filepath.Join(module, "check.yaml"), []byte(pipelineYAML(t, preset)), 0600))
						digest, err := registry.DigestDirectory(module)
						require.NoError(t, err)
						lock := models.NewLockfile()
						lock.UpsertGrader(models.LockfileGrader{Ref: ref, Commit: commit, Digest: digest, URL: "https://not-contacted.invalid"})
						require.NoError(t, models.WriteLockfile(filepath.Join(dir, models.LockfileName), lock))
					}
					report := Inspect(path, Options{})
					if source == "cache" && schema.state == Verified {
						// The existing runtime YAML merge drops typed matchers. Do not
						// change that behavior or claim the resulting config is valid.
						require.True(t, hasDiagnostic(report, "grader.merge", Invalid), "%+v", report.Diagnostics)
						require.True(t, report.Failed(false))
						require.False(t, report.Complete)
					} else if schema.state == Verified {
						require.True(t, report.Complete, "%+v", report.Diagnostics)
						require.False(t, report.Failed(source != "checkpoint"), "%+v", report.Diagnostics)
					} else {
						require.True(t, hasDiagnostic(report, code, schema.state), "%+v", report.Diagnostics)
						require.False(t, report.Complete)
						require.Equal(t, schema.state == Invalid, report.Failed(false), "%+v", report.Diagnostics)
					}
					if schema.name == "file" && source == "eval" {
						_, err := models.LoadEvalSpec(path)
						require.NoError(t, err, "legacy readable external schema must remain valid")
					}
					require.Zero(t, requests.Load(), "HTTP schema loads must remain exactly zero")
				})
			}
		}
	}
}

func pipelineYAML(t *testing.T, value any) string {
	t.Helper()
	data, err := yaml.Marshal(value)
	require.NoError(t, err)
	return "\n" + string(data)
}

func TestInspectMCPSourceSchemaPipeline(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	external := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(external, []byte(`{"type":"object"}`), 0600))
	for _, ref := range []string{testutil.FileURL(external), server.URL} {
		for _, field := range []string{"input_schema", "match_schema", "return"} {
			t.Run(field+"/"+ref, func(t *testing.T) {
				response := map[string]any{"return": map[string]any{"ready": true}}
				tool := map[string]any{"responses": []any{response}}
				if field == "input_schema" {
					tool[field] = map[string]any{"$ref": ref}
				} else {
					response[field] = map[string]any{"$ref": ref}
				}
				eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + pipelineYAML(t, map[string]any{"mcp_mocks": []any{map[string]any{"name": "local", "tools": map[string]any{"read": tool}}}})
				path, _ := writeSuite(t, eval, validTask)
				report := Inspect(path, Options{})
				require.False(t, report.Failed(false), "%+v", report.Diagnostics)
				if field == "return" {
					require.False(t, hasDiagnostic(report, "eval.configuration", Unresolved), "%+v", report.Diagnostics)
					require.False(t, hasDiagnostic(report, "mock.mcp", Unresolved), "%+v", report.Diagnostics)
					require.False(t, hasDiagnostic(report, "mock.input_schema", Unresolved), "%+v", report.Diagnostics)
					require.True(t, report.Complete)
				} else {
					require.True(t, report.Failed(true), "%+v", report.Diagnostics)
					require.Equal(t, field == "input_schema", report.Complete)
				}
				require.Zero(t, requests.Load(), "HTTP schema loads must remain exactly zero")
			})
		}
	}
}
