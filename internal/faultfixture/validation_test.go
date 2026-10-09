package faultfixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFiniteSourceValidation(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		validate func([]byte, Format, string) error
		want     string
	}{
		{"CLI transient recovery", `{"args":["read"],"sequence":[{"stderr":"temporary","exit_code":1},{"stdout":{"ready":true},"exit_code":0,"delay_ms":0}]}`, ValidateCommandResponse, ""},
		{"CLI empty success", `{"args":[],"sequence":[{"stdout":"","exit_code":0}]}`, ValidateCommandResponse, ""},
		{"CLI regex environment directory", `{"args_regex":["read",".+"],"environment":{"ENABLED":"yes"},"workdir":".","sequence":[{"fixture":"fixtures/result.json"}]}`, ValidateCommandResponse, ""},
		{"CLI output payload keys", `{"args":[],"sequence":[{"stdout":{"sequence":[],"args":[],"error":null}}]}`, ValidateCommandResponse, ""},
		{"MCP transient recovery", `{"match":{"id":"item"},"sequence":[{"error":"temporary"},{"return":{"ready":true},"delay_ms":0}]}`, ValidateMCPResponse, ""},
		{"MCP explicit null output", `{"sequence":[{"return":null}]}`, ValidateMCPResponse, ""},
		{"MCP payload keys", `{"match_regex":{"id":"^item"},"sequence":[{"return":{"sequence":[],"match":[],"error":null}}]}`, ValidateMCPResponse, ""},
		{"MCP schema matcher", `{"match_schema":{"type":"object","properties":{"count":{"type":"integer","minimum":1}}},"sequence":[{"return":{}}]}`, ValidateMCPResponse, ""},
		{"empty sequence", `{"args":[],"sequence":[]}`, ValidateCommandResponse, "at least one"},
		{"null sequence", `{"sequence":null}`, ValidateMCPResponse, "not null"},
		{"scalar sequence", `{"args":[],"sequence":false}`, ValidateCommandResponse, "sequence array"},
		{"scalar step", `{"sequence":[false]}`, ValidateMCPResponse, "JSON object"},
		{"null step", `{"args":[],"sequence":[null]}`, ValidateCommandResponse, "JSON object"},
		{"empty step", `{"args":[],"sequence":[{}]}`, ValidateCommandResponse, "explicitly define"},
		{"delay only step", `{"sequence":[{"delay_ms":1}]}`, ValidateMCPResponse, "explicitly define"},
		{"recursive step", `{"args":[],"sequence":[{"sequence":[],"exit_code":0}]}`, ValidateCommandResponse, "nested matcher/sequence"},
		{"nested args", `{"args":[],"sequence":[{"args":[],"exit_code":0}]}`, ValidateCommandResponse, "nested matcher/sequence"},
		{"nested match", `{"sequence":[{"match":{},"return":null}]}`, ValidateMCPResponse, "nested matcher/sequence"},
		{"typo delay", `{"args":[],"sequence":[{"delay_mss":1,"exit_code":0}]}`, ValidateCommandResponse, "delay_mss"},
		{"unknown matcher", `{"arg":[],"sequence":[{"exit_code":0}]}`, ValidateCommandResponse, "unknown fault response"},
		{"stdout fixture field presence", `{"args":[],"sequence":[{"stdout":null,"fixture":""}]}`, ValidateCommandResponse, "both fixture and stdout"},
		{"MCP dual field presence", `{"sequence":[{"return":null,"error":""}]}`, ValidateMCPResponse, "exactly one"},
		{"empty MCP error", `{"sequence":[{"error":""}]}`, ValidateMCPResponse, "non-empty"},
		{"null MCP error", `{"sequence":[{"error":null}]}`, ValidateMCPResponse, "non-empty"},
		{"MCP error wrong type", `{"sequence":[{"error":false}]}`, ValidateMCPResponse, "decoding MCP fixture error"},
		{"CLI negative exit", `{"args":[],"sequence":[{"exit_code":-1}]}`, ValidateCommandResponse, "between 0 and 255"},
		{"CLI exit overflow", `{"args":[],"sequence":[{"exit_code":256}]}`, ValidateCommandResponse, "between 0 and 255"},
		{"CLI null exit", `{"args":[],"sequence":[{"exit_code":null}]}`, ValidateCommandResponse, "not null"},
		{"CLI null stderr", `{"args":[],"sequence":[{"stderr":null}]}`, ValidateCommandResponse, "not null"},
		{"CLI null fixture", `{"args":[],"sequence":[{"fixture":null}]}`, ValidateCommandResponse, "not null"},
		{"CLI fixture traversal", `{"args":[],"sequence":[{"fixture":"../result.json"}]}`, ValidateCommandResponse, "relative path"},
		{"CLI missing matcher", `{"sequence":[{"exit_code":0}]}`, ValidateCommandResponse, "exactly one"},
		{"CLI both matchers", `{"args":[],"args_regex":[],"sequence":[{"exit_code":0}]}`, ValidateCommandResponse, "exactly one"},
		{"CLI bad regex", `{"args_regex":["["],"sequence":[{"exit_code":0}]}`, ValidateCommandResponse, "invalid"},
		{"MCP bad regex", `{"match_regex":{"id":"["},"sequence":[{"return":null}]}`, ValidateMCPResponse, "invalid regex"},
		{"MCP bad schema", `{"match_schema":{"type":42},"sequence":[{"return":null}]}`, ValidateMCPResponse, "match_schema is invalid"},
		{"CLI typed output", `{"args":[],"sequence":[{"exit_code":"zero"}]}`, ValidateCommandResponse, "decoding command"},
		{"MCP typed matcher", `{"match":[],"sequence":[{"return":null}]}`, ValidateMCPResponse, "decoding MCP"},
	}
	for _, test := range tests {
		for _, format := range []Format{JSON, YAML} {
			t.Run(test.name+"/"+string(format), func(t *testing.T) {
				err := test.validate(sourceForFormat(t, test.data, format), format, models.ScenarioSchemaVersion)
				if test.want == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, test.want)
				}
			})
		}
	}
}

func TestMixedSequenceRejectsZeroNullAndEmptyFieldPresence(t *testing.T) {
	for _, test := range []struct {
		name     string
		matcher  string
		step     string
		fields   map[string]any
		validate func([]byte, Format, string) error
	}{
		{"CLI", `"args":[],`, `{"exit_code":0}`, map[string]any{"stdout": "", "stderr": "", "exit_code": 0, "fixture": ""}, ValidateCommandResponse},
		{"MCP", "", `{"return":null}`, map[string]any{"return": nil, "error": ""}, ValidateMCPResponse},
	} {
		for field, value := range test.fields {
			for _, format := range []Format{JSON, YAML} {
				t.Run(test.name+"/"+field+"/"+string(format), func(t *testing.T) {
					raw, err := json.Marshal(value)
					require.NoError(t, err)
					data := fmt.Sprintf(`{%s%q:%s,"sequence":[%s]}`, test.matcher, field, raw, test.step)
					require.ErrorContains(t, test.validate(sourceForFormat(t, data, format), format, models.ScenarioSchemaVersion), "cannot mix sequence")
				})
			}
		}
	}
}

func TestVersionIsExplicitAndNeverInferred(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.4", "1.99", "2.1", "3.0", "invalid"} {
		for _, format := range []Format{JSON, YAML} {
			t.Run(version+"/"+string(format), func(t *testing.T) {
				command := sourceForFormat(t, `{"args":[],"sequence":[{"exit_code":0}]}`, format)
				mcp := sourceForFormat(t, `{"sequence":[{"return":null}]}`, format)
				require.ErrorContains(t, ValidateCommandResponse(command, format, version), "explicit enclosing scenario schemaVersion 2.0")
				require.ErrorContains(t, ValidateMCPResponse(mcp, format, version), "explicit enclosing scenario schemaVersion 2.0")
			})
		}
	}
}

func TestDelayBoundsBeforeTimerCreation(t *testing.T) {
	for _, raw := range []string{"-1", fmt.Sprint(faultsequence.MaxDelayMilliseconds + 1), "9223372036854775808", "0.5", "null", "false", `"0"`} {
		for _, format := range []Format{JSON, YAML} {
			t.Run(raw+"/"+string(format), func(t *testing.T) {
				data := fmt.Sprintf(`{"args":[],"sequence":[{"exit_code":0,"delay_ms":%s}]}`, raw)
				require.Error(t, ValidateCommandResponse(sourceForFormat(t, data, format), format, models.ScenarioSchemaVersion))
			})
		}
	}
	data := fmt.Sprintf(`{"sequence":[{"return":null,"delay_ms":%d}]}`, faultsequence.MaxDelayMilliseconds)
	for _, format := range []Format{JSON, YAML} {
		require.NoError(t, ValidateMCPResponse(sourceForFormat(t, data, format), format, models.ScenarioSchemaVersion))
	}
}

func TestTopLevelDelayNeverDisappearsAsAnUnknownLegacyField(t *testing.T) {
	for _, value := range []string{"0", "-1", "null", `"invalid"`} {
		for _, format := range []Format{JSON, YAML} {
			command := fmt.Sprintf(`{"args":[],"delay_ms":%s}`, value)
			mcp := fmt.Sprintf(`{"return":null,"delay_ms":%s}`, value)
			require.ErrorContains(t, ValidateCommandResponse(sourceForFormat(t, command, format), format, "1.4"), "only supported inside")
			require.ErrorContains(t, ValidateMCPResponse(sourceForFormat(t, mcp, format), format, "1.4"), "only supported inside")
		}
	}
}

func TestYAMLFloatTagsAreNotCanonicalizedIntoIntegers(t *testing.T) {
	for _, value := range []string{"1.0", "1.0000000000000001", "!!float 1", "1e0"} {
		for _, field := range []string{"delay_ms", "exit_code"} {
			data := fmt.Sprintf("args: []\nsequence:\n  - stdout: ready\n    %s: %s\n", field, value)
			require.ErrorContains(t, ValidateCommandResponse([]byte(data), YAML, models.ScenarioSchemaVersion), "YAML integer")
			merged := fmt.Sprintf("args: []\nsequence:\n  - <<: &defaults {%s: %s}\n    stdout: ready\n", field, value)
			require.ErrorContains(t, ValidateCommandResponse([]byte(merged), YAML, models.ScenarioSchemaVersion), "YAML integer")
		}
		mcp := fmt.Sprintf("sequence:\n  - return: null\n    delay_ms: %s\n", value)
		require.ErrorContains(t, ValidateMCPResponse([]byte(mcp), YAML, models.ScenarioSchemaVersion), "YAML integer")
	}
	require.NoError(t, ValidateMCPResponse([]byte("sequence:\n  - return: {delay_ms: 1.0}\n    delay_ms: !!int '1'\n"), YAML, models.ScenarioSchemaVersion))
}

func TestYAMLMergePresenceAndCycles(t *testing.T) {
	require.ErrorContains(t, ValidateCommandResponse([]byte(`
<<: &defaults {stdout: ''}
args: []
sequence: [{exit_code: 0}]
`), YAML, models.ScenarioSchemaVersion), "cannot mix sequence")
	require.NoError(t, ValidateCommandResponse([]byte(`
args: []
sequence:
  - &failure {stderr: refused, exit_code: 1}
  - <<: *failure
    exit_code: 0
    stdout: recovered
`), YAML, models.ScenarioSchemaVersion))
	require.Error(t, ValidateCommandResponse([]byte(`
args: []
sequence: [{exit_code: 0, exit_code: 1}]
`), YAML, models.ScenarioSchemaVersion))
	require.Error(t, ValidateMCPResponse([]byte(`
sequence:
  - return: &loop {value: *loop}
`), YAML, models.ScenarioSchemaVersion))
}

func TestJSONDuplicateFieldsCannotLaunderFiniteModes(t *testing.T) {
	require.ErrorContains(t, ValidateCommandResponse([]byte(`{"args":[],"sequence":null,"sequence":[{"exit_code":0}]}`), JSON, models.ScenarioSchemaVersion), "duplicate field")
	require.ErrorContains(t, ValidateMCPResponse([]byte(`{"sequence":[{"return":null,"return":{}}]}`), JSON, models.ScenarioSchemaVersion), "duplicate field")
	require.NoError(t, ValidateCommandResponse([]byte(`{"args":[],"stdout":"first","stdout":"last"}`), JSON, "1.4"))
	require.NoError(t, ValidateMCPResponse([]byte(`{"sequence":[{"return":{"value":1,"value":2}}]}`), JSON, models.ScenarioSchemaVersion))
}

func TestLegacyUnknownFieldsWarnAndScenarioFieldsReject(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	data := []byte(`{"args":[],"unknown_field":"private-value"}`)
	require.NoError(t, ValidateCommandResponse(data, JSON, "1.4"))
	require.Contains(t, output.String(), "unknown schema field ignored")
	require.NotContains(t, output.String(), "private-value")
	require.ErrorContains(t, ValidateCommandResponse(data, JSON, models.ScenarioSchemaVersion), "unknown fault response")
	for _, format := range []Format{JSON, YAML} {
		require.NoError(t, ValidateCommandResponse(sourceForFormat(t, `{"args":[],"stderr":null,"exit_code":null}`, format), format, "1.4"))
		require.NoError(t, ValidateMCPResponse(sourceForFormat(t, `{"return":null,"error":"legacy failure"}`, format), format, "1.4"))
	}
}

func TestMalformedAndMultipleDocuments(t *testing.T) {
	for _, data := range []string{"", "not-json", "null", "[]", `{"args":`, `{"args":[]`, `{"args":[]} {}`, `{"args":[]} invalid`} {
		require.Error(t, ValidateCommandResponse([]byte(data), JSON, models.ScenarioSchemaVersion), data)
	}
	for _, data := range []string{"null", "[]", "args: [", "args: []\n---\nargs: []", "args: []\n---\n["} {
		require.Error(t, ValidateCommandResponse([]byte(data), YAML, models.ScenarioSchemaVersion), data)
	}
	require.ErrorContains(t, ValidateCommandResponse([]byte(`{"args":[]}`), "toml", models.ScenarioSchemaVersion), "unsupported")
}

func TestMCPSourceValidationCannotLoadExternalSchemas(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write([]byte(`{"type":"object"}`)); err != nil {
			t.Errorf("writing schema sentinel: %v", err)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "valid-schema.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"object"}`), 0600))
	fileURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	for _, location := range []string{fileURI, server.URL + "/schema"} {
		for _, format := range []Format{JSON, YAML} {
			for _, mode := range []string{`"return":null`, `"sequence":[{"return":null}]`} {
				data := fmt.Sprintf(`{"match_schema":{"$ref":%q},%s}`, location, mode)
				err := ValidateMCPResponse(sourceForFormat(t, data, format), format, models.ScenarioSchemaVersion)
				var loadErr *jsonschema.LoadURLError
				require.ErrorAs(t, err, &loadErr)
				require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
				require.Zero(t, requests.Load())
			}
			payload := fmt.Sprintf(`{"sequence":[{"return":{"$ref":%q}}]}`, location)
			require.NoError(t, ValidateMCPResponse(sourceForFormat(t, payload, format), format, models.ScenarioSchemaVersion))
		}
	}
	require.NoError(t, os.Remove(path))
	data := fmt.Sprintf(`{"match_schema":{"$ref":%q},"return":null}`, fileURI)
	var loadErr *jsonschema.LoadURLError
	require.ErrorAs(t, ValidateMCPResponse([]byte(data), JSON, models.ScenarioSchemaVersion), &loadErr)
	require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference, "file existence must not affect source validation")
	local := `{"match_schema":{"$defs":{"value":{"type":"object"}},"$ref":"#/$defs/value"},"sequence":[{"return":null}]}`
	for _, format := range []Format{JSON, YAML} {
		require.NoError(t, ValidateMCPResponse(sourceForFormat(t, local, format), format, models.ScenarioSchemaVersion))
	}
}

func sourceForFormat(t *testing.T, data string, format Format) []byte {
	t.Helper()
	if format == JSON {
		return []byte(data)
	}
	var value yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(data), &value))
	encoded, err := yaml.Marshal(&value)
	require.NoError(t, err)
	return encoded
}
