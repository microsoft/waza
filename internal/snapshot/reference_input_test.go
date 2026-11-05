package snapshot_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestSnapshotRejectsAuthoredProfileAtEveryNativeBoundary(t *testing.T) {
	text := "finite"
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, &text)
	require.NoError(t, err)
	for _, version := range []string{"1.1", "1.0", "", "1.2"} {
		t.Run(version, func(t *testing.T) {
			manifest := input.Manifest()
			manifest.Version = version
			snap := snapshot.Snapshot{
				EvalID: manifest.Origin.EvalID,
				Task:   snapshot.SnapshotTask{TestID: "task", RunNumber: 1}, Evidence: manifest,
			}
			data, err := json.Marshal(snap)
			require.NoError(t, err)
			_, err = snapshot.ParseSnapshot(data, "authored-input")
			require.Error(t, err)
			var decoded snapshot.Snapshot
			require.Error(t, json.Unmarshal(data, &decoded))
			require.Equal(t, snapshot.Snapshot{}, decoded)
			hidden := strings.TrimSuffix(string(data), "}") + `,"evidence":null}`
			_, err = snapshot.ParseSnapshot([]byte(hidden), "hidden-authored-input")
			require.Error(t, err)
			require.Error(t, json.Unmarshal([]byte(hidden), &decoded))
			require.Error(t, snapshot.VerifyWorkspace(&snap, manifest.Origin, manifest.SHA256, []string{"file.txt"}))
			workspace, err := snapshot.MaterializeWorkspace(&snap, manifest.Origin, manifest.SHA256, []string{"file.txt"})
			require.Error(t, err)
			require.Nil(t, workspace)
		})
	}
}

func TestSnapshotOrdinaryCaptureAndRegradeNativeReaderRegression(t *testing.T) {
	snap, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "native-eval", Task: &models.TestCase{TestID: "task"}, ExecutionMode: "mock",
		Run: &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed},
	})
	require.NoError(t, err)
	for _, regrade := range []bool{false, true} {
		if regrade {
			snap.Evidence, err = evidence.Regrade(snap.Evidence)
			require.NoError(t, err)
		}
		data, err := json.Marshal(snap)
		require.NoError(t, err)
		parsed, err := snapshot.ParseSnapshot(data, "native-snapshot")
		require.NoError(t, err)
		require.NoError(t, evidence.ValidateNative(parsed.Evidence))
		require.Equal(t, "1.0", parsed.Evidence.Version)
		var apiDecoded snapshot.Snapshot
		require.NoError(t, json.Unmarshal(data, &apiDecoded))
		require.Equal(t, parsed.Evidence, apiDecoded.Evidence)
	}
}

func TestSnapshotRejectsHiddenAuthoredJSONAliases(t *testing.T) {
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, nil)
	require.NoError(t, err)
	authored, err := json.Marshal(input.Manifest())
	require.NoError(t, err)
	for name, data := range map[string]string{
		"evidence overwritten": `{"evidence":` + string(authored) + `,"Evidence":null}`,
		"uppercase first":      `{"Evidence":null,"evidence":` + string(authored) + `}`,
		"uppercase only":       `{"EVIDENCE":` + string(authored) + `}`,
		"mixed case":           `{"evidence":` + string(authored) + `,"eViDeNcE":null}`,
		"escaped alias":        `{"evidence":` + string(authored) + `,"Evidenc\u0065":null}`,
		"manifest and origin laundered": `{"evidence":` + strings.Replace(
			strings.Replace(string(authored), `"version":"1.1"`, `"version":"1.1","Version":"1.0"`, 1),
			`"eval_id":"reference:subject"`, `"eval_id":"reference:subject","Eval_id":"native-eval"`, 1) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			var snap snapshot.Snapshot
			require.Error(t, json.Unmarshal([]byte(data), &snap))
			require.Equal(t, snapshot.Snapshot{}, snap)
			_, err := snapshot.ParseSnapshot([]byte(data), "aliased-authored-snapshot")
			require.Error(t, err)
			path := filepath.Join(t.TempDir(), "snapshot.json")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			_, err = snapshot.LoadSnapshotFile(path)
			require.Error(t, err)
			manifest := input.Manifest()
			require.Error(t, snapshot.VerifyWorkspace(&snap, manifest.Origin, manifest.SHA256, []string{"file.txt"}))
			workspace, err := snapshot.MaterializeWorkspace(&snap, manifest.Origin, manifest.SHA256, []string{"file.txt"})
			require.Error(t, err)
			require.Nil(t, workspace)
		})
	}
}

func TestSnapshotNativeJSONKeysPreserveCanonicalCompatibility(t *testing.T) {
	for _, data := range []string{
		`{"evidence":null,"futureField":true,"FutureField":false}`,
		`{"result":{"finalOutput":"native"},"task":{"testId":"task","runNumber":1}}`,
	} {
		_, err := snapshot.ParseSnapshot([]byte(data), "canonical-native")
		require.NoError(t, err)
	}
	for _, data := range []string{
		`{"Evidence":null}`,
		`{"workspaceFiles":[],"WorkspaceFiles":[]}`,
		`{"task":{"TestId":"task"}}`,
		`{"result":{"FinalOutput":"native"}}`,
	} {
		var snap snapshot.Snapshot
		require.ErrorContains(t, json.Unmarshal([]byte(data), &snap), "exact spelling")
		_, err := snapshot.ParseSnapshot([]byte(data), "aliased-native")
		require.ErrorContains(t, err, "exact spelling")
	}
	snap := snapshot.Snapshot{EvalID: "unchanged"}
	require.ErrorContains(t, json.Unmarshal([]byte(`{"evidence":null,"Evidence":null}`), &snap), "exact spelling")
	require.Equal(t, "unchanged", snap.EvalID)
}
