package preflight

import (
	"encoding/json"
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
			inspectMCP(report, spec, t.TempDir())
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
