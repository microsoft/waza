package orchestration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestSnapshotPublicationErrorRetainsResidualPathWithoutRecordedSuccess(t *testing.T) {
	for _, path := range []string{"", "private/evidence-residual.json"} {
		t.Run(path, func(t *testing.T) {
			run := models.RunResult{RunNumber: 1, Attempts: 1}
			snap, err := snapshot.Capture(snapshot.CaptureInput{EvalID: "eval",
				Task: &models.TestCase{TestID: "task"}, Run: &run, ExecutionMode: "mock"})
			require.NoError(t, err)
			recordSnapshotPublication(&run, snap, path, errors.New("late close failed"))
			require.Equal(t, path, run.SnapshotPath)
			require.Equal(t, "unavailable", run.Evidence.Artifacts[0].Availability)
			require.Equal(t, "capture_failed", run.Evidence.Diagnostics[0].Code)
			require.NoError(t, evidence.Bind(run.Evidence, "eval", "task", 1, 1, false))
			require.Equal(t, snap.Evidence.Origin, run.Evidence.Origin)
			require.Error(t, evidence.Bind(run.Evidence, "other", "task", 1, 1, false))
			require.Error(t, evidence.Bind(run.Evidence, "eval", "other", 1, 1, false))
			require.Empty(t, run.Evidence.Artifacts[0].Document)
			require.Empty(t, run.Evidence.Artifacts[0].Pointer)
			data, err := json.Marshal(run)
			require.NoError(t, err)
			if path != "" {
				require.Contains(t, string(data), `"snapshot_path":"private/evidence-residual.json"`)
			}
			recordSnapshotPublication(&run, snap, "published.json", nil)
			require.Equal(t, snap.Evidence, run.Evidence)
		})
	}
}
