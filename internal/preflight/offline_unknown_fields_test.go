package preflight

import (
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOfflineUnknownFieldsRetainNativeCompatibility(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, kind := range []string{"tool_calls", "tool_constraint"} {
		field := "expect"
		if kind == "tool_constraint" {
			field = "expect_tools"
		}
		for _, level := range []string{"top", "config", "expectation", "none"} {
			for _, reverse := range []bool{false, true} {
				for _, ref := range refs {
					t.Run(fmt.Sprintf("%s/%s/reverse=%t/%s", kind, level, reverse, ref), func(t *testing.T) {
						schema := map[string]any{"type": "object"}
						expectation := map[string]any{"tool": "read", "args": map[string]any{
							"value": map[string]any{"json_schema": schema},
						}}
						other := map[string]any{"tool": "write"}
						expectations := []any{expectation, other}
						if reverse {
							expectations[0], expectations[1] = expectations[1], expectations[0]
						}
						config := map[string]any{field: expectations}
						declaration := map[string]any{"name": "preset", "type": kind, "config": config}
						switch level {
						case "top":
							declaration["future_field"] = true
						case "config":
							config["future_field"] = true
						case "expectation":
							expectation["future_field"] = true
						}
						safe := []byte(pipelineYAML(t, declaration))
						var native models.GraderConfig
						require.NoError(t, yaml.Unmarshal(safe, &native), "native same-major unknown fields are warnings, not rejection")
						require.NoError(t, graders.ValidateConfig(native.Identifier, native.Parameters))
						delete(schema, "type")
						schema["$ref"] = ref
						external := []byte(pipelineYAML(t, declaration))
						_, err := models.DecodeGraderConfigOffline(external, graders.ValidateConfig)
						require.Error(t, err)
						invalid, blocked := schemaloader.ClassifyError(err)
						require.False(t, invalid, "offline must not invent rejection absent in native decoding")
						require.True(t, blocked)
						var override models.GraderConfig
						require.NoError(t, yaml.Unmarshal([]byte(pipelineYAML(t, map[string]any{
							"ref": "github.com/example/checks/check.yaml@v1", "config": config,
						})), &override))
						_, err = registry.MergeGraderConfigOffline(native, override)
						require.Error(t, err)
						invalid, blocked = schemaloader.ClassifyError(err)
						require.False(t, invalid)
						require.True(t, blocked)
						report := &Report{Complete: true}
						report.add("schema", report.schemaFailureState(err), "preset.yaml", "", "", "Unavailable schema.", "")
						require.False(t, report.Failed(false))
						require.True(t, report.Failed(true))
						require.False(t, report.Complete)
						require.Zero(t, requests.Load())
					})
				}
			}
		}
	}
}
