package preflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestInspectSnapshotBootstrap(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, kind := range []string{"missing", "existing", "directory", "escape", "missing-parent"} {
			t.Run(fmt.Sprintf("%s/update=%t", kind, update), func(t *testing.T) {
				dir := t.TempDir()
				snapshot := "record.txt"
				path := filepath.Join(dir, snapshot)
				switch kind {
				case "existing":
					require.NoError(t, os.WriteFile(path, []byte("before\n"), 0o600))
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				case "escape":
					snapshot = "../outside.txt"
				case "missing-parent":
					snapshot = "absent/record.txt"
				}
				before, err := os.ReadDir(dir)
				require.NoError(t, err)
				report := &Report{Complete: true}
				parameters := models.DiffGraderParameters{
					ContextDir: dir, UpdateSnapshots: update,
					ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "record.txt", Snapshot: snapshot}},
				}
				inspectGraders(report, []models.GraderConfig{{Identifier: "snapshot", Kind: models.GraderKindDiff, Parameters: parameters}}, "eval.yaml", "", dir)
				invalid := kind != "existing" && (kind != "missing" || !update)
				require.Equal(t, invalid, report.Failed(false), "%+v", report.Diagnostics)
				require.Equal(t, kind == "missing" && update, hasDiagnostic(report, "grader.snapshot_bootstrap", Unresolved))
				after, err := os.ReadDir(dir)
				require.NoError(t, err)
				require.Equal(t, before, after, "preflight must not create snapshot files or parents")
				if kind == "missing" {
					_, err := os.Lstat(path)
					require.True(t, errors.Is(err, os.ErrNotExist))
				}
				if kind == "existing" {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, "before\n", string(data))
				}
			})
		}
	}
}

func TestInspectMCPFixtureInputSchemaTypes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema any
		valid  bool
	}{
		{"omitted", nil, true},
		{"object", map[string]any{"type": "object"}, true},
		{"string", "not-a-schema", false},
		{"number", 42, false},
		{"boolean", true, false},
		{"false", false, false},
		{"array", []any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tool := map[string]any{"responses": []any{map[string]any{"return": "ready"}}}
			if tc.schema != nil {
				tool["input_schema"] = tc.schema
			}
			data, err := json.Marshal(tool)
			require.NoError(t, err)
			path := filepath.Join(dir, "read.json")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			spec := &models.EvalSpec{MCPMocks: []models.MCPMockConfig{{Name: "fixture", Fixtures: dir}}}
			report := &Report{Complete: true}
			inspectMCP(report, spec, dir, nil)
			require.Equal(t, !tc.valid, report.Failed(false), "%+v", report.Diagnostics)
			require.Equal(t, !tc.valid, hasDiagnostic(report, "mock.input_schema", Invalid))
			require.Len(t, report.Dependencies, 1)
			want := Invalid
			if tc.valid {
				want = Verified
			}
			require.Equal(t, want, report.Dependencies[0].State)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, data, after, "fixture inspection must not rewrite its schema")
		})
	}
}

func TestInspectMCPFixtureInvalidSchemaPrecedence(t *testing.T) {
	for _, invalidFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-first=%t", invalidFirst), func(t *testing.T) {
			dir := t.TempDir()
			schemas := []any{
				map[string]any{"$ref": "https://invalid.example/schema.json"},
				"not-a-schema",
			}
			if invalidFirst {
				schemas[0], schemas[1] = schemas[1], schemas[0]
			}
			for i, schema := range schemas {
				data, err := json.Marshal(map[string]any{
					"input_schema": schema,
					"responses":    []any{map[string]any{"return": "ready"}},
				})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("tool-%d.json", i)), data, 0o600))
			}
			report := &Report{Complete: true}
			inspectMCP(report, &models.EvalSpec{MCPMocks: []models.MCPMockConfig{{Name: "fixture", Fixtures: dir}}}, dir, nil)
			require.True(t, report.Failed(false))
			require.False(t, report.Complete)
			require.True(t, hasDiagnostic(report, "mock.input_schema", Invalid))
			require.True(t, hasDiagnostic(report, "mock.input_schema", Unresolved))
			require.Len(t, report.Dependencies, 1)
			require.Equal(t, Invalid, report.Dependencies[0].State)
		})
	}
}

func TestInspectGraderSchemaCompleteness(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name string
		doc  map[string]any
		want State
	}{
		{"self contained", map[string]any{"type": "object"}, Verified},
		{"local fragment", map[string]any{"$defs": map[string]any{"value": map[string]any{"type": "object"}}, "$ref": "#/$defs/value"}, Verified},
		{"external ref", map[string]any{"$ref": server.URL}, Unresolved},
		{"external metaschema", map[string]any{"$schema": server.URL, "type": "object"}, Unresolved},
		{"malformed", map[string]any{"type": "not-a-type"}, Invalid},
	} {
		for _, file := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/file=%t", tc.name, file), func(t *testing.T) {
				dir := t.TempDir()
				parameters := models.JSONSchemaGraderParameters{Schema: tc.doc}
				if file {
					data, err := json.Marshal(tc.doc)
					require.NoError(t, err)
					parameters = models.JSONSchemaGraderParameters{SchemaFile: filepath.Join(dir, "schema.json")}
					require.NoError(t, os.WriteFile(parameters.SchemaFile, data, 0600))
				}
				report := &Report{Complete: true}
				inspectGraders(report, []models.GraderConfig{{Identifier: "schema", Kind: models.GraderKindJSONSchema, Parameters: parameters}}, "eval.yaml", "", dir)
				require.Equal(t, tc.want != Unresolved, report.Complete)
				require.Equal(t, tc.want == Invalid, report.Failed(false), "%+v", report.Diagnostics)
				if tc.want != Verified {
					require.True(t, hasDiagnostic(report, "grader.schema", tc.want), "%+v", report.Diagnostics)
					if !file {
						require.True(t, hasDiagnostic(report, "grader.configuration", tc.want), "%+v", report.Diagnostics)
					}
				}

				require.Zero(t, requests.Load(), "schema checks must never contact an HTTP service")
			})
		}
	}
}

func TestInspectMCPUnavailableSchemaCompleteness(t *testing.T) {
	for _, field := range []string{"input_schema", "match_schema", "return"} {
		t.Run(field, func(t *testing.T) {
			schema := map[string]any{"$ref": "https://not-contacted.invalid/schema.json"}
			response := models.MCPMockResponse{Return: map[string]any{"ready": true}}
			tool := models.MCPMockTool{}
			switch field {
			case "input_schema":
				tool.InputSchema = schema
			case "match_schema":
				response.MatchSchema = schema
			case "return":
				response.Return = schema
			}
			tool.Responses = []models.MCPMockResponse{response}
			spec := &models.EvalSpec{MCPMocks: []models.MCPMockConfig{{Name: "mock", Tools: map[string]models.MCPMockTool{"read": tool}}}}
			report := &Report{Complete: true}
			inspectMCP(report, spec, t.TempDir(), nil)
			require.Equal(t, field == "return", report.Complete)
			require.False(t, report.Failed(false), "%+v", report.Diagnostics)
			require.Len(t, report.Dependencies, 1)
			if field == "return" {
				require.Equal(t, Verified, report.Dependencies[0].State)
			} else {
				require.Equal(t, Unresolved, report.Dependencies[0].State)
				require.True(t, report.Failed(true))
				code := "mock.mcp"
				if field == "input_schema" {
					code = "mock.input_schema"
				}
				require.True(t, hasDiagnostic(report, code, Unresolved), "%+v", report.Diagnostics)
			}
		})
	}
}
