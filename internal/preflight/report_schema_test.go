package preflight

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestPreflightReportSchemaCoversSuccessFailureAndPartialInventory(t *testing.T) {
	var schemaDoc map[string]any
	require.NoError(t, json.Unmarshal([]byte(schemas.PreflightSchemaJSON), &schemaDoc))
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	require.NoError(t, compiler.AddResource("preflight.schema.json", schemaDoc))
	schema, err := compiler.Compile("preflight.schema.json")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, task string
		missing    bool
	}{
		{"valid", validTask, false},
		{"invalid authored metadata", strings.ReplaceAll(validTask, "category: outcome", "category: invalid-category"), false},
		{"uncovered", strings.ReplaceAll(validTask, "    checks:\n      - scope: task\n        grader: state", "    checks: []"), false},
		{"partial", validTask, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, dir := writeSuite(t, validEval, tc.task)
			if tc.missing {
				path = filepath.Join(dir, "absent.yaml")
			}
			data, err := json.Marshal(Inspect(path, Options{}))
			require.NoError(t, err)
			var report any
			require.NoError(t, json.Unmarshal(data, &report))
			require.NoError(t, schema.Validate(report))
		})
	}
}
