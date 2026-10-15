package models_test

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFaultSourceModelBoundary(t *testing.T) {
	tests := []struct {
		name     string
		validate func([]byte, models.FaultSourceFormat, string) error
		json     string
		yaml     string
		version  string
		want     string
	}{
		{
			name: "legacy command", validate: models.ValidateCommandFaultResponse,
			json: `{"args":[],"stderr":null,"exit_code":null}`,
			yaml: "args: []\nstderr: null\nexit_code: null\n", version: "1.4",
		},
		{
			name: "legacy MCP", validate: models.ValidateMCPFaultResponse,
			json: `{"return":null,"error":"legacy failure"}`,
			yaml: "return: null\nerror: legacy failure\n", version: "1.4",
		},
		{
			name: "finite command", validate: models.ValidateCommandFaultResponse,
			json:    `{"args":[],"sequence":[{"stderr":"temporary","exit_code":1},{"stdout":"","exit_code":0,"delay_ms":0}]}`,
			yaml:    "args: []\nsequence:\n  - {stderr: temporary, exit_code: 1}\n  - {stdout: '', exit_code: 0, delay_ms: 0}\n",
			version: models.ScenarioSchemaVersion,
		},
		{
			name: "finite MCP", validate: models.ValidateMCPFaultResponse,
			json:    `{"sequence":[{"error":"temporary"},{"return":null,"delay_ms":0}]}`,
			yaml:    "sequence:\n  - {error: temporary}\n  - {return: null, delay_ms: 0}\n",
			version: models.ScenarioSchemaVersion,
		},
		{
			name: "legacy command invalid matcher", validate: models.ValidateCommandFaultResponse,
			json: `{"args_regex":["["]}`, yaml: "args_regex: ['[']\n", version: "1.4", want: "invalid",
		},
		{
			name: "legacy MCP invalid matcher", validate: models.ValidateMCPFaultResponse,
			json: `{"match_regex":{"id":"["}}`, yaml: "match_regex: {id: '['}\n", version: "1.4", want: "invalid regex",
		},
		{
			name: "finite command raw presence", validate: models.ValidateCommandFaultResponse,
			json: `{"args":[],"stdout":null,"sequence":[{"exit_code":0}]}`,
			yaml: "args: []\nstdout: null\nsequence: [{exit_code: 0}]\n", version: models.ScenarioSchemaVersion,
			want: `fault response cannot mix sequence with field "stdout", even when empty, zero or null`,
		},
		{
			name: "finite MCP dual presence", validate: models.ValidateMCPFaultResponse,
			json: `{"sequence":[{"return":null,"error":""}]}`,
			yaml: "sequence: [{return: null, error: ''}]\n", version: models.ScenarioSchemaVersion,
			want: "fault sequence[0]: MCP step must specify exactly one of return or error",
		},
		{
			name: "finite exact version", validate: models.ValidateMCPFaultResponse,
			json: `{"sequence":[{"return":null}]}`, yaml: "sequence: [{return: null}]\n", version: "2.1",
			want: "fault sequence requires explicit enclosing scenario schemaVersion 2.0",
		},
	}
	for _, tt := range tests {
		for _, source := range []struct {
			format models.FaultSourceFormat
			data   string
		}{{models.FaultSourceJSON, tt.json}, {models.FaultSourceYAML, tt.yaml}} {
			t.Run(tt.name+"/"+string(source.format), func(t *testing.T) {
				err := tt.validate([]byte(source.data), source.format, tt.version)
				if tt.want == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tt.want)
				}
			})
		}
	}
}

func TestFaultSourceSharedDecoders(t *testing.T) {
	data := []byte(`{"args":[],"stdout":null,"exit_code":0,"stdout":""}`)
	direct, duplicates, err := models.DecodeFaultObject(data)
	require.NoError(t, err)
	require.Equal(t, []string{"stdout"}, duplicates)
	require.Equal(t, `""`, string(direct["stdout"]))
	require.Equal(t, "0", string(direct["exit_code"]))
	source, sourceDuplicates, err := models.DecodeFaultSource(data, models.FaultSourceJSON)
	require.NoError(t, err)
	require.Equal(t, direct, source)
	require.Equal(t, duplicates, sourceDuplicates)

	_, _, err = models.DecodeFaultSource([]byte("args: []\nsequence:\n  - <<: &defaults {exit_code: !!float 1}\n    stdout: ready\n"), models.FaultSourceYAML)
	require.EqualError(t, err, "fault sequence[0].exit_code must be a YAML integer, not a float")
}
