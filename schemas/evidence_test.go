package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestEvidenceSchemaAcceptsCapturedAndRegradedManifest(t *testing.T) {
	var schema any
	require.NoError(t, json.Unmarshal([]byte(schemas.EvidenceManifestSchemaJSON), &schema))
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("manifest.json", schema))
	compiled, err := compiler.Compile("manifest.json")
	require.NoError(t, err)
	snap, err := snapshot.Capture(snapshot.CaptureInput{EvalID: "eval",
		Task: &models.TestCase{TestID: "task"}, ExecutionMode: "mock",
		Run: &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed},
	})
	require.NoError(t, err)
	for _, regraded := range []bool{false, true} {
		manifest := snap.Evidence
		if regraded {
			manifest, err = evidence.Regrade(manifest)
			require.NoError(t, err)
		}
		data, err := json.Marshal(manifest)
		require.NoError(t, err)
		var instance any
		require.NoError(t, json.Unmarshal(data, &instance))
		require.NoError(t, compiled.Validate(instance))
		object, ok := instance.(map[string]any)
		require.True(t, ok)
		object["unhashed"] = "extra"
		require.Error(t, compiled.Validate(instance))
		delete(object, "unhashed")
		object["version"] = "2.0"
		require.Error(t, compiled.Validate(instance))
	}
}
