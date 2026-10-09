package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestEvidenceCaptureRecordsOperationalPhaseAndActualAttempt(t *testing.T) {
	for _, category := range []string{"setup", "execution", "grader"} {
		t.Run(category, func(t *testing.T) {
			spec := &models.EvalSpec{Config: models.Config{TrialsPerTask: 1, TimeoutSec: 30}}
			task := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "hello"}}
			var engine execution.AgentEngine = &trackingEngine{}
			switch category {
			case "setup":
				task.TimeoutSec = new(-1)
			case "execution":
				engine = &errorOnCallEngine{errOnCall: 1}
			case "grader":
				task.Validators = []models.ValidatorInline{{Identifier: "bad", Kind: models.GraderKind("unknown")}}
			}
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine,
				WithSnapshotWriter(snapshot.NewWriter(t.TempDir())))
			runner.evalRunID = "eval"
			run := runner.executeAttempt(context.Background(), task, 1, 3)
			require.Equal(t, models.StatusError, run.Status)
			require.NoError(t, evidence.Validate(run.Evidence))
			require.Equal(t, 3, run.Evidence.Origin.AttemptCount)
			require.Equal(t, category, run.Evidence.Diagnostics[0].Category)
			require.NotEmpty(t, run.SnapshotPath)
		})
	}
}

func TestRequestedCaptureFailureIsNotHistoricalAbsence(t *testing.T) {
	root := t.TempDir()
	invalidDestination := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(invalidDestination, []byte("existing"), 0o600))
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30}}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(root)), &trackingEngine{},
		WithSkipGraders(), WithSnapshotWriter(snapshot.NewWriter(invalidDestination)))
	run := runner.executeRun(context.Background(), &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "hello"}}, 1)
	require.NotNil(t, run.Evidence)
	require.Empty(t, run.SnapshotPath)
	require.NoError(t, evidence.Validate(run.Evidence))
	require.Equal(t, "capture_failed", run.Evidence.Diagnostics[0].Code)
	require.Equal(t, "unavailable", run.Evidence.Artifacts[0].Availability)
	require.Equal(t, models.StatusSkipped, run.Status)
}
