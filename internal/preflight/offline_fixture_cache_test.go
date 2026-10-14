package preflight

import (
	"encoding/json"
	"fmt"
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
)

func offlineFixtureCacheReferences(t *testing.T) ([]string, *atomic.Int64) {
	t.Helper()
	requests := new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	file := filepath.Join(t.TempDir(), "unread-schema.json")
	require.NoError(t, os.WriteFile(file, []byte("not JSON"), 0o600))
	t.Cleanup(func() {
		after, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Equal(t, "not JSON", string(after))
		require.Zero(t, requests.Load(), "offline fixture/cache inspection must never request schemas")
	})
	return []string{server.URL, testutil.FileURL(file)}, requests
}

func TestInspectOfflineFixtureMixedResponseStates(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, ref := range refs {
		for _, reverse := range []bool{false, true} {
			for _, mode := range []string{"same response", "separate responses", "external only", "invalid only", "valid"} {
				t.Run(fmt.Sprintf("%s/reverse=%t/%s", ref, reverse, mode), func(t *testing.T) {
					external := map[string]any{"match_schema": map[string]any{"$ref": ref}, "return": "ready"}
					invalid := map[string]any{"match_regex": map[string]string{"value": "["}, "return": "ready"}
					responses := []any{external, invalid}
					if reverse {
						responses[0], responses[1] = responses[1], responses[0]
					}
					switch mode {
					case "same response":
						external["match_regex"] = invalid["match_regex"]
						responses = []any{external}
					case "external only":
						responses = []any{external}
					case "invalid only":
						responses = []any{invalid}
					case "valid":
						responses = []any{map[string]any{"match_schema": map[string]any{"type": "object"}, "match_regex": map[string]string{"value": "^ready"}, "return": "ready"}}
					}
					eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") +
						"\nmcp_mocks: [{name: fixture, fixtures: mock-fixtures}]\n"
					path, dir := writeSuite(t, eval, validTask)
					fixtures := filepath.Join(dir, "mock-fixtures")
					require.NoError(t, os.Mkdir(fixtures, 0o700))
					data, err := json.Marshal(map[string]any{"responses": responses})
					require.NoError(t, err)
					fixture := filepath.Join(fixtures, "read.json")
					require.NoError(t, os.WriteFile(fixture, data, 0o600))
					report := Inspect(path, Options{})
					wantInvalid := mode == "same response" || mode == "separate responses" || mode == "invalid only"
					hasExternal := mode != "invalid only" && mode != "valid"
					require.Equal(t, wantInvalid, report.Failed(false), "%+v", report.Diagnostics)
					require.Equal(t, !hasExternal, report.Complete, "%+v", report.Diagnostics)
					if wantInvalid {
						require.True(t, hasDiagnostic(report, "mock.mcp", Invalid), "%+v", report.Diagnostics)
					}
					if hasExternal && wantInvalid {
						require.True(t, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
					} else if hasExternal {
						require.True(t, hasDiagnostic(report, "mock.mcp", Unresolved), "%+v", report.Diagnostics)
					}
					require.Equal(t, wantInvalid || hasExternal, report.Dependencies[0].State != Verified)
					if wantInvalid {
						require.Equal(t, Invalid, report.Dependencies[0].State)
					}
					after, err := os.ReadFile(fixture)
					require.NoError(t, err)
					require.Equal(t, data, after)
					after, err = os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, eval, string(after))
					require.Zero(t, requests.Load())
				})
			}
		}
	}
}

func TestInspectOfflineLockedGraderMixedStates(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, ref := range refs {
		for _, source := range []string{"cache", "merge"} {
			for _, reverse := range []bool{false, true} {
				for _, declaration := range []struct{ kind, field string }{
					{"tool_calls", "expect"},
					{"tool_constraint", "expect_tools"},
					{"tool_constraint", "reject_tools"},
					{"tool_constraint", "allow_only"},
				} {
					problems := []string{"blank tool", "tool regex", "valid external", "invalid only", "valid only"}
					if declaration.kind == "tool_calls" {
						problems = append(problems, "negative min", "negative max", "inverted bounds")
					} else {
						problems = append(problems, "command_pattern", "skill_pattern", "path_pattern")
					}
					for _, problem := range problems {
						t.Run(fmt.Sprintf("%s/%s/%s/%s/reverse=%t/%s", ref, source, declaration.kind, declaration.field, reverse, problem), func(t *testing.T) {
							external := map[string]any{"tool": "read", "args": map[string]any{"value": map[string]any{"json_schema": map[string]any{"$ref": ref}}}}
							other := map[string]any{"tool": "write"}
							config := map[string]any{}
							switch problem {
							case "blank tool":
								other["tool"] = " "
							case "tool regex":
								other["tool"] = "["
							case "negative min", "invalid only":
								if declaration.kind == "tool_calls" {
									config["min_calls"] = -1
								} else {
									other["command_pattern"] = "["
								}
							case "negative max":
								config["max_calls"] = -1
							case "inverted bounds":
								config["min_calls"], config["max_calls"] = 2, 1
							case "command_pattern", "skill_pattern", "path_pattern":
								other[problem] = "["
							}
							hasExternal := problem != "invalid only" && problem != "valid only"
							if !hasExternal {
								delete(external, "args")
							}
							expectations := []any{external, other}
							if reverse {
								expectations[0], expectations[1] = expectations[1], expectations[0]
							}
							config[declaration.field] = expectations
							preset := map[string]any{"name": "preset", "type": declaration.kind, "config": config}
							const moduleRef = "github.com/example/checks/check.yaml@v1"
							override := map[string]any{"name": "locked", "ref": moduleRef}
							code := "grader.cache_integrity"
							if source == "merge" {
								code = "grader.merge"
								override["config"] = config
								preset["config"] = map[string]any{declaration.field: []any{map[string]any{"tool": "read"}}}
							}
							declarations := []any{override, map[string]any{"name": "safe", "type": "text", "config": map[string]any{"contains": []string{"ready"}}}}
							if reverse {
								declarations[0], declarations[1] = declarations[1], declarations[0]
							}
							eval := validEval + pipelineYAML(t, map[string]any{"graders": declarations})
							path, dir := writeSuite(t, eval, validTask)
							cache := t.TempDir()
							t.Setenv("WAZA_MODULE_CACHE", cache)
							commit := strings.Repeat("a", 40)
							module := filepath.Join(cache, "github.com", "example", "checks", commit)
							require.NoError(t, os.MkdirAll(module, 0o700))
							data := []byte(pipelineYAML(t, preset))
							presetPath := filepath.Join(module, "check.yaml")
							require.NoError(t, os.WriteFile(presetPath, data, 0o600))
							digest, err := registry.DigestDirectory(module)
							require.NoError(t, err)
							lock := models.NewLockfile()
							lock.UpsertGrader(models.LockfileGrader{Ref: moduleRef, Commit: commit, Digest: digest, URL: "https://not-contacted.invalid"})
							lockPath := filepath.Join(dir, models.LockfileName)
							require.NoError(t, models.WriteLockfile(lockPath, lock))
							lockBytes, err := os.ReadFile(lockPath)
							require.NoError(t, err)
							report := Inspect(path, Options{})
							wantInvalid := problem != "valid external" && problem != "valid only"
							if declaration.field == "allow_only" && problem == "tool regex" {
								wantInvalid = false
							}
							require.Equal(t, wantInvalid, report.Failed(false), "%+v", report.Diagnostics)
							require.Equal(t, wantInvalid || hasExternal, report.Failed(true), "%+v", report.Diagnostics)
							if hasExternal {
								require.False(t, report.Complete)
								state := Unresolved
								if wantInvalid {
									state = Invalid
									require.True(t, hasDiagnostic(report, "schema.external_reference", Unresolved), "%+v", report.Diagnostics)
								}
								require.True(t, hasDiagnostic(report, code, state), "%+v", report.Diagnostics)
								require.Equal(t, state, report.Dependencies[0].State)
							} else if !wantInvalid {
								require.True(t, report.Complete, "%+v", report.Diagnostics)
							}
							after, err := os.ReadFile(path)
							require.NoError(t, err)
							require.Equal(t, eval, string(after))
							after, err = os.ReadFile(presetPath)
							require.NoError(t, err)
							require.Equal(t, data, after)
							after, err = os.ReadFile(lockPath)
							require.NoError(t, err)
							require.Equal(t, lockBytes, after)
							afterDigest, err := registry.DigestDirectory(module)
							require.NoError(t, err)
							require.Equal(t, digest, afterDigest)
							require.Zero(t, requests.Load())
						})
					}
				}
			}
		}
	}
}
