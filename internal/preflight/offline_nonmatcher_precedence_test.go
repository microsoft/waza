package preflight

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestInspectOfflineSameGraderNonMatcherPrecedence(t *testing.T) {
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
			for _, declaration := range []struct{ kind, field string }{
				{"tool_calls", "expect"},
				{"tool_constraint", "expect_tools"},
				{"tool_constraint", "reject_tools"},
				{"tool_constraint", "allow_only"},
			} {
				problems := []string{"blank tool", "tool regex", "valid", "external only"}
				if declaration.kind == "tool_calls" {
					problems = append(problems, "negative min", "negative max", "inverted bounds")
				} else {
					problems = append(problems, "command_pattern", "skill_pattern", "path_pattern", "other group command")
					if declaration.field != "allow_only" {
						problems = append(problems, "deny all")
					}
				}
				for _, reverse := range []bool{false, true} {
					for _, problem := range problems {
						for _, sameEntry := range []bool{false, true} {
							t.Run(fmt.Sprintf("%s/%s/%s/%s/reverse=%t/same=%t/%s", target, scope, declaration.kind, declaration.field, reverse, sameEntry, problem), func(t *testing.T) {
								external := map[string]any{"tool": "read", "args": map[string]any{"value": map[string]any{"json_schema": map[string]any{"$ref": target}}}}
								other := map[string]any{"tool": "write"}
								bad := other
								if sameEntry {
									bad = external
								}
								config := map[string]any{}
								switch problem {
								case "blank tool":
									bad["tool"] = " "
								case "tool regex":
									bad["tool"] = "["
								case "negative min":
									config["min_calls"] = -1
								case "negative max":
									config["max_calls"] = -1
								case "inverted bounds":
									config["min_calls"], config["max_calls"] = 2, 1
								case "command_pattern", "skill_pattern", "path_pattern":
									bad[problem] = "["
								case "other group command":
									field := "allow_only"
									if declaration.field == field {
										field = "expect_tools"
									}
									config[field] = []any{map[string]any{"tool": "write", "command_pattern": "["}}
								case "deny all":
									config["allow_only"] = []any{}
								}
								expectations := []any{external, other}
								if reverse {
									expectations[0], expectations[1] = expectations[1], expectations[0]
								}
								if problem == "external only" {
									expectations = []any{external}
								}
								config[declaration.field] = expectations
								graders := []any{map[string]any{"name": "mixed", "type": declaration.kind, "config": config}}
								eval, task, code := validEval, "id: task\nname: Task\ninputs: {prompt: Inspect.}\n", "eval.configuration"
								switch scope {
								case "eval":
									eval += pipelineYAML(t, map[string]any{"graders": graders})
								case "task":
									task += pipelineYAML(t, map[string]any{"graders": graders})
									code = "task.configuration"
								case "checkpoint":
									task += pipelineYAML(t, map[string]any{"checkpoints": []any{map[string]any{"after_turn": 1, "graders": graders}}})
									code = "task.configuration"
								}
								path, dir := writeSuite(t, eval, task)
								report := Inspect(path, Options{})
								wantInvalid := problem != "valid" && problem != "external only" && problem != "deny all"
								if declaration.field == "allow_only" && problem == "tool regex" {
									wantInvalid = false // allow_only tool names are literal, not regexes.
								}
								require.Equal(t, wantInvalid, report.Failed(false), "%+v", report.Diagnostics)
								require.True(t, report.Failed(true))
								require.False(t, report.Complete)
								require.True(t, hasOfflineDiagnostic(report), "%+v", report.Diagnostics)
								if wantInvalid {
									require.True(t, hasDiagnostic(report, code, Invalid), "%+v", report.Diagnostics)
								}
								after, err := os.ReadFile(path)
								require.NoError(t, err)
								require.Equal(t, eval, string(after))
								after, err = os.ReadFile(filepath.Join(dir, "task.yaml"))
								require.NoError(t, err)
								require.Equal(t, task, string(after))
								require.Zero(t, requests.Load(), "static field validation must not fetch external schemas")
							})
						}
					}
				}
			}
		}
	}
	after, err := os.ReadFile(externalFile)
	require.NoError(t, err)
	require.Equal(t, "not JSON", string(after))
}
