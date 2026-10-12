package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilitySnapshotReferences(t *testing.T) {
	root := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1")
	outcome, err := models.LoadEvaluationOutcome(filepath.Join(root, "results-1.4.json"))
	require.NoError(t, err)
	task := outcome.TestOutcomes[0]
	run := task.Runs[0]
	snap, err := LoadSnapshotFile(filepath.Join(root, run.SnapshotPath))
	require.NoError(t, err)
	require.Equal(t, outcome.RunID, snap.EvalID)
	require.Equal(t, outcome.BenchName, snap.EvalName)
	require.Equal(t, outcome.SkillTested, snap.Skill)
	require.Equal(t, task.TestID, snap.Task.TestID)
	require.Equal(t, task.Golden, snap.Task.Golden)
	require.Equal(t, run.RunNumber, snap.Task.RunNumber)
	require.Equal(t, run.ToolEvents, snap.ToolEvents)
	require.Equal(t, run.FinalOutput, snap.Result.FinalOutput)
	require.Equal(t, run.Validations, snap.Result.Validations)
	require.Equal(t, run.Status, snap.Result.Status)
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	for _, version := range []string{"", "1.0", "1.99", "2.0", "bad"} {
		t.Run(version, func(t *testing.T) {
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			if version == "" {
				delete(wire, "schemaVersion")
			} else {
				wire["schemaVersion"], err = json.Marshal(version)
				require.NoError(t, err)
			}
			wire["future_field"] = json.RawMessage(`true`)
			b, err := json.Marshal(wire)
			require.NoError(t, err)
			loaded, err := ParseSnapshot(b, "corpus")
			if version == "2.0" || version == "bad" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.True(t, Compare(snap, loaded, true).Match)
			require.Equal(t, snap.Task, loaded.Task)
			require.Equal(t, snap.Result, loaded.Result)
		})
	}
	changed := *snap
	changed.ToolEvents = append([]models.ToolEvent(nil), snap.ToolEvents...)
	changed.ToolEvents[0].Args = map[string]any{"path": "different.txt"}
	require.False(t, Compare(snap, &changed, true).Match)
	roundtrip := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(roundtrip, data, 0o600))
	loaded, err := LoadSnapshotFile(roundtrip)
	require.NoError(t, err)
	require.Equal(t, snap, loaded, "fixed fixture has no volatile fields to normalize")
}
