package mcpmock

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func writeSourceFixture(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

func jsonSource(data string) SourceResponse {
	return SourceResponse{Format: models.FaultSourceJSON, Data: []byte(data)}
}

func sourceMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", value)
	return object
}

func TestSourceConfigLexicalWholeToolOverrides(t *testing.T) {
	dir := t.TempDir()
	writeSourceFixture(t, dir, "a.json", `{"tools":{
		"lookup":{"description":"early","input_schema":{"type":"object"},"responses":[{"sequence":[{"return":0,"delay_ms":0.0}]}]},
		"inline":{"description":"external","responses":[{"sequence":[{"return":null,"error":""}]}]},
		"bundle":{"responses":[{ "return" : 1.00 }]}
	}}`)
	writeSourceFixture(t, dir, "b/lookup.json", `{"responses":[{ "return": "single" }]}`)
	writeSourceFixture(t, dir, "c.json", `{"tools":{"lookup":{"description":"latest","responses":[{ "return": "bundle" }]}}}`)
	writeSourceFixture(t, dir, ".hidden/ignored.json", `invalid`)
	writeSourceFixture(t, dir, "ignored.JSON", `invalid`)
	writeSourceFixture(t, dir, "ignored.yaml", `invalid`)
	mock := models.MCPMockConfig{
		Name: " capture ", Fixtures: filepath.Base(dir),
		Tools: map[string]models.MCPMockTool{
			"inline": {Description: "inline", InputSchema: map[string]any{"type": "object"}, Responses: []models.MCPMockResponse{{}}},
		},
	}
	inline := map[string][]SourceResponse{"inline": {jsonSource(`{ "return": "inline" }`)}}
	got, err := FromEvalConfigWithSources(mock, filepath.Dir(dir), inline, "")
	require.NoError(t, err, "overwritten finite fragments must not be validated")
	require.Equal(t, "capture", got.Config.Name)
	require.Len(t, got.Config.Tools, 3)
	require.Equal(t, "latest", got.Config.Tools["lookup"].Description)
	require.Nil(t, got.Config.Tools["lookup"].InputSchema, "whole-tool replacement removes prior metadata")
	require.Equal(t, "bundle", got.Config.Tools["lookup"].Responses[0].Return)
	require.Equal(t, `{ "return": "bundle" }`, string(got.Responses["lookup"][0].Data))
	require.Equal(t, "inline", got.Config.Tools["inline"].Description)
	require.Equal(t, "inline", got.Config.Tools["inline"].Responses[0].Return)
	require.Equal(t, inline["inline"], got.Responses["inline"])
	require.Equal(t, `{ "return" : 1.00 }`, string(got.Responses["bundle"][0].Data))
}

func TestSourceConfigNativeBundleRecognition(t *testing.T) {
	tests := []struct {
		name string
		data string
		tool string
	}{
		{"nonempty bundle", `{"tools":{"named":{"responses":[{"return":"ok"}]}},"responses":[{"return":"ignored"}]}`, "named"},
		{"empty tools single", `{"tools":{},"responses":[{"return":"ok"}]}`, "fixture"},
		{"null tools single", `{"tools":null,"responses":[{"return":"ok"}]}`, "fixture"},
		{"wrong tools type single", `{"tools":[],"responses":[{"return":"ok"}]}`, "fixture"},
		{"typed invalid bundle single", `{"tools":{"named":{"description":1,"responses":[{"return":"ignored"}]}},"responses":[{"return":"ok"}]}`, "fixture"},
		{"duplicate response arrays", `{"responses":[{"return":"ignored"}],"responses":[{ "return":"ok" }]}`, "fixture"},
		{"duplicate tool name", `{"tools":{"named":{"responses":[{"return":"ignored"}]},"named":{"responses":[{ "return":"ok" }]}}}`, "named"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSourceFixture(t, dir, "fixture.json", tt.data)
			mock := models.MCPMockConfig{Name: "native", Fixtures: dir}
			legacy, err := FromEvalConfig(mock, "")
			require.NoError(t, err)
			got, err := FromEvalConfigWithSources(mock, "", nil, "")
			require.NoError(t, err)
			require.Equal(t, legacy, got.Config)
			require.Len(t, got.Responses, 1)
			require.Equal(t, "ok", got.Config.Tools[tt.tool].Responses[0].Return)
		})
	}
}

func TestSourceConfigOwnsCapture(t *testing.T) {
	dir := t.TempDir()
	fragment := `{ "return" : {"n":1.00,"items":[{"state":"captured"}]} }`
	writeSourceFixture(t, dir, "external.json", `{"input_schema":{"type":"object"},"responses":[`+fragment+`]}`)
	mock := models.MCPMockConfig{Name: "own", Fixtures: dir, Tools: map[string]models.MCPMockTool{
		"inline": {
			InputSchema: map[string]any{"properties": map[string]any{"id": map[string]any{"type": "string"}}},
			Responses:   []models.MCPMockResponse{{Return: map[string]any{"state": "modeled"}}},
		},
	}}
	inline := map[string][]SourceResponse{"inline": {jsonSource(`{"return":{"state":"source","items":[{"id":"original"}]}}`)}}
	got, err := FromEvalConfigWithSources(mock, "", inline, "")
	require.NoError(t, err)
	snapshot := bytes.Clone(got.Responses["inline"][0].Data)
	writeSourceFixture(t, dir, "external.json", `not valid JSON anymore`)
	require.NoError(t, os.Remove(filepath.Join(dir, "external.json")))
	inline["inline"][0].Data[0] = '!'
	inline["inline"][0].Format = models.FaultSourceYAML
	sourceMap(t, sourceMap(t, mock.Tools["inline"].InputSchema["properties"])["id"])["type"] = "number"
	sourceMap(t, mock.Tools["inline"].Responses[0].Return)["state"] = "mutated"
	delete(mock.Tools, "inline")
	require.Equal(t, fragment, string(got.Responses["external"][0].Data))
	require.Equal(t, []any{map[string]any{"state": "captured"}}, sourceMap(t, got.Config.Tools["external"].Responses[0].Return)["items"])
	require.Equal(t, snapshot, got.Responses["inline"][0].Data)
	require.Equal(t, models.FaultSourceJSON, got.Responses["inline"][0].Format)
	require.Equal(t, map[string]any{"properties": map[string]any{"id": map[string]any{"type": "string"}}}, got.Config.Tools["inline"].InputSchema)
	require.Equal(t, "source", sourceMap(t, got.Config.Tools["inline"].Responses[0].Return)["state"])
	got.Responses["inline"][0].Data[0] = '?'
	require.Equal(t, "source", sourceMap(t, got.Config.Tools["inline"].Responses[0].Return)["state"], "raw and typed data must not alias")
}

type sourceCaptureMutation func() error

func (mutate sourceCaptureMutation) MarshalJSON() ([]byte, error) {
	if err := mutate(); err != nil {
		return nil, err
	}
	return []byte(`"captured"`), nil
}

func TestSourceConfigDoesNotRereadDuringResolution(t *testing.T) {
	dir := t.TempDir()
	fragment := `{ "sequence": [{"error":"retry"},{"return":null,"delay_ms":0}] }`
	writeSourceFixture(t, dir, "read.json", `{"responses":[`+fragment+`]}`)
	mutated := false
	mock := models.MCPMockConfig{Name: "snapshot", Fixtures: dir, Tools: map[string]models.MCPMockTool{
		"inline": {
			InputSchema: map[string]any{"description": sourceCaptureMutation(func() error {
				mutated = true
				return os.WriteFile(filepath.Join(dir, "read.json"), []byte(`{"responses":[{"sequence":[{"return":null,"delay_ms":0.0}]}]}`), 0o600)
			})},
			Responses: []models.MCPMockResponse{{}},
		},
	}}
	got, err := FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{"inline": {jsonSource(`{"return":"ready"}`)}}, models.ScenarioSchemaVersion)
	require.NoError(t, err, "validation must use the bytes captured before inline metadata resolution mutated the fixture")
	require.True(t, mutated)
	require.Equal(t, fragment, string(got.Responses["read"][0].Data))
	require.Empty(t, got.Config.Tools["read"].Responses[0])
	mock.Tools = nil
	got, err = FromEvalConfigWithSources(mock, "", nil, models.ScenarioSchemaVersion)
	require.ErrorContains(t, err, "must be an integer", "a fresh resolution must observe the changed fixture")
	require.Nil(t, got)
}

func TestSourceConfigFiniteRawValidation(t *testing.T) {
	tests := []struct {
		name, response, version, want string
	}{
		{"finite accepted", `{ "sequence" : [{"error":"retry"},{"return":null,"delay_ms":0}] }`, models.ScenarioSchemaVersion, ""},
		{"missing version", `{"sequence":[{"return":null}]}`, "", "requires explicit enclosing scenario"},
		{"wrong version", `{"sequence":[{"return":null}]}`, "2.1", "requires explicit enclosing scenario"},
		{"duplicate sequence", `{"sequence":[{"return":null}],"sequence":[{"return":null}]}`, models.ScenarioSchemaVersion, "duplicate field"},
		{"duplicate step output", `{"sequence":[{"return":0,"return":null}]}`, models.ScenarioSchemaVersion, "duplicate field"},
		{"duplicate step delay", `{"sequence":[{"return":null,"delay_ms":0,"delay_ms":0}]}`, models.ScenarioSchemaVersion, "duplicate field"},
		{"float delay", `{"sequence":[{"return":null,"delay_ms":0.0}]}`, models.ScenarioSchemaVersion, "must be an integer"},
		{"exponent delay", `{"sequence":[{"return":null,"delay_ms":1e0}]}`, models.ScenarioSchemaVersion, "must be an integer"},
		{"null delay", `{"sequence":[{"return":null,"delay_ms":null}]}`, models.ScenarioSchemaVersion, "must be an integer"},
		{"missing step output", `{"sequence":[{"delay_ms":0}]}`, models.ScenarioSchemaVersion, "must explicitly define"},
		{"null outer return", `{"return":null,"sequence":[{"return":null}]}`, models.ScenarioSchemaVersion, "cannot mix sequence"},
		{"empty outer error", `{"error":"","sequence":[{"return":null}]}`, models.ScenarioSchemaVersion, "cannot mix sequence"},
		{"dual step output", `{"sequence":[{"return":null,"error":""}]}`, models.ScenarioSchemaVersion, "exactly one"},
		{"empty step error", `{"sequence":[{"error":""}]}`, models.ScenarioSchemaVersion, "non-empty string"},
		{"legacy duplicate output", `{"return":0,"return":1.00}`, "", ""},
	}
	for _, tt := range tests {
		for _, bundle := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/single", true: "/bundle"}[bundle], func(t *testing.T) {
				dir := t.TempDir()
				data := `{"responses":[` + tt.response + `]}`
				if bundle {
					data = `{"tools":{"read":` + data + `}}`
				}
				writeSourceFixture(t, dir, "read.json", data)
				got, err := FromEvalConfigWithSources(models.MCPMockConfig{Name: "finite", Fixtures: dir}, "", nil, tt.version)
				if tt.want != "" {
					require.ErrorContains(t, err, tt.want)
					require.ErrorContains(t, err, `tool "read" response 0`)
					require.Nil(t, got)
					return
				}
				require.NoError(t, err)
				require.Equal(t, tt.response, string(got.Responses["read"][0].Data))
				require.Len(t, got.Config.Tools["read"].Responses, 1)
				if tt.name == "finite accepted" {
					require.Empty(t, got.Config.Tools["read"].Responses[0], "no public sequence execution is registered")
				}
			})
		}
	}
}

func TestSourceConfigInlineSourcesRequiredAndAuthoritative(t *testing.T) {
	mock := models.MCPMockConfig{Name: "inline", Tools: map[string]models.MCPMockTool{
		"read": {Responses: []models.MCPMockResponse{{Return: "modeled"}}},
	}}
	for _, sources := range []map[string][]SourceResponse{
		nil, {}, {"other": {jsonSource(`{"return":"ok"}`)}}, {"read": nil},
		{"read": {jsonSource(`{}`), jsonSource(`{}`)}},
	} {
		got, err := FromEvalConfigWithSources(mock, "", sources, "")
		require.ErrorContains(t, err, "inline source responses must be supplied")
		require.Nil(t, got)
	}
	got, err := FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{
		"read": {{Format: models.FaultSourceYAML, Data: []byte("sequence:\n  - return: null\n    delay_ms: 0.0\n")}},
	}, models.ScenarioSchemaVersion)
	require.ErrorContains(t, err, "must be a YAML integer")
	require.Nil(t, got)
	got, err = FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{
		"read":   {{Format: models.FaultSourceYAML, Data: []byte("return: {state: source}\n")}},
		"unused": {jsonSource(`not JSON`)},
	}, "")
	require.NoError(t, err, "only modeled inline tools select source entries")
	require.Equal(t, map[string]any{"state": "source"}, got.Config.Tools["read"].Responses[0].Return)
	require.Equal(t, "return: {state: source}\n", string(got.Responses["read"][0].Data))
}

func TestSourceConfigOfflineMatchersAndInertReturnReferences(t *testing.T) {
	dir := t.TempDir()
	writeSourceFixture(t, dir, "schema.json", `{"type":"object"}`)
	fileURI := testutil.FileURL(filepath.Join(dir, "schema.json"))
	for _, fixture := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "external"}[fixture], func(t *testing.T) {
			mock := models.MCPMockConfig{Name: "offline", Tools: map[string]models.MCPMockTool{
				"read": {Responses: []models.MCPMockResponse{{}}},
			}}
			resolve := func(response string) (*SourceConfig, error) {
				if fixture {
					fixtures := filepath.Join(dir, "fixtures")
					writeSourceFixture(t, fixtures, "read.json", `{"responses":[`+response+`]}`)
					return FromEvalConfigWithSources(models.MCPMockConfig{Name: "offline", Fixtures: fixtures}, "", nil, "")
				}
				return FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{"read": {jsonSource(response)}}, "")
			}
			got, err := resolve(`{"return":{"$ref":"file:///unavailable-return.json","sequence":[{"delay_ms":0.0}]}}`)
			require.NoError(t, err, "payload properties are inert, not fault configuration or schemas")
			require.Equal(t, "file:///unavailable-return.json", sourceMap(t, got.Config.Tools["read"].Responses[0].Return)["$ref"])
			encoded, err := json.Marshal(map[string]any{"match_schema": map[string]any{"$ref": fileURI}})
			require.NoError(t, err)
			got, err = resolve(string(encoded))
			require.Nil(t, got)
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, err, &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			got, err = resolve(`{"match_schema":{"$defs":{"args":{"type":"object"}},"$ref":"#/$defs/args"},"return":null}`)
			require.NoError(t, err, "internal schema references remain usable offline")
			require.NotNil(t, got)
			got, err = resolve(`{"match_regex":{"id":"["}}`)
			require.ErrorContains(t, err, "invalid regex")
			require.Nil(t, got)
		})
	}
}

func TestSourceConfigErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name, data, want string
	}{
		{"malformed", `{`, "unexpected"},
		{"invalid typed response", `{"responses":[{"error":1}]}`, "cannot unmarshal"},
		{"scalar response", `{"responses":[1]}`, "cannot unmarshal"},
		{"empty tool", `{"responses":[]}`, "at least one response"},
		{"null tool", `null`, "at least one response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			writeSourceFixture(t, dir, "read.json", tt.data)
			got, err := FromEvalConfigWithSources(models.MCPMockConfig{Name: "bad", Fixtures: dir}, "", nil, "")
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, got)
		})
	}
	for _, tt := range []struct {
		mock models.MCPMockConfig
		want string
	}{
		{models.MCPMockConfig{}, "missing name"},
		{models.MCPMockConfig{Name: "empty"}, "at least one tool"},
		{models.MCPMockConfig{Name: "missing", Fixtures: filepath.Join(dir, "missing")}, "fixtures"},
		{models.MCPMockConfig{Name: "file", Fixtures: filepath.Join(dir, "read.json")}, "not a directory"},
	} {
		got, err := FromEvalConfigWithSources(tt.mock, "", nil, "")
		require.ErrorContains(t, err, tt.want)
		require.Nil(t, got)
	}
	mock := models.MCPMockConfig{Name: "empty", Tools: map[string]models.MCPMockTool{"read": {}}}
	got, err := FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{"read": nil}, "")
	require.ErrorContains(t, err, "at least one response")
	require.Nil(t, got)
	mock.Tools["read"] = models.MCPMockTool{InputSchema: map[string]any{"bad": make(chan int)}, Responses: []models.MCPMockResponse{{}}}
	got, err = FromEvalConfigWithSources(mock, "", map[string][]SourceResponse{"read": {jsonSource(`{}`)}}, "")
	require.ErrorContains(t, err, "metadata")
	require.Nil(t, got)
}

func TestSourceConfigLeavesLegacyAPIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeSourceFixture(t, dir, "read.json", `{"responses":[{"return":null,"sequence":[{"return":null,"delay_ms":0.0}]}]}`)
	mock := models.MCPMockConfig{Name: "legacy", Fixtures: dir}
	for _, resolve := range []func(models.MCPMockConfig, string) (*Config, error){FromEvalConfig, FromEvalConfigOffline} {
		got, err := resolve(mock, "")
		require.NoError(t, err, "legacy APIs still ignore sequence and do not demand a version")
		require.Empty(t, got.Tools["read"].Responses[0])
	}
	got, err := FromEvalConfigWithSources(mock, "", nil, models.ScenarioSchemaVersion)
	require.ErrorContains(t, err, "cannot mix sequence")
	require.Nil(t, got)
}
