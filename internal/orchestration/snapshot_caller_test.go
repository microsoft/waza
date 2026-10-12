package orchestration

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
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

type snapshotCallerWriter struct {
	*snapshot.Writer
	problem   error
	temporary bool
	published string
}

func (w *snapshotCallerWriter) Write(snap *snapshot.Snapshot) (string, error) {
	path, err := w.Writer.Write(snap)
	if err != nil {
		return path, err
	}
	if w.temporary {
		temporary := filepath.Join(w.Root(), "."+filepath.Base(path)+".tmp")
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
		if err := os.Rename(path, temporary); err != nil {
			return "", err
		}
		path = temporary
	}
	w.published = path
	return path, w.problem
}

func snapshotCallerInputs(t *testing.T) (*config.EvalConfig, *models.TestCase, *execution.ExecutionRequest, models.RunResult) {
	t.Helper()
	spec := &models.EvalSpec{SpecIdentity: models.SpecIdentity{Name: "snapshot-caller"}}
	task := &models.TestCase{TestID: "snapshot-task", DisplayName: "Snapshot task"}
	request := &execution.ExecutionRequest{Message: "recorded prompt"}
	run := models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed, FinalOutput: "recorded output"}
	return config.NewEvalConfig(spec), task, request, run
}

func TestSnapshotCallerRetainsResidualPublication(t *testing.T) {
	for _, name := range []string{"success", "late-error", "temporary-cleanup-error"} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			cfg, task, request, run := snapshotCallerInputs(t)
			runner := NewEvalRunner(cfg, nil)
			writer := &snapshotCallerWriter{Writer: snapshot.NewWriter(t.TempDir())}
			switch name {
			case "late-error":
				writer.problem = errors.New("snapshot: close private evidence directory failed")
			case "temporary-cleanup-error":
				writer.temporary = true
				writer.problem = errors.Join(
					errors.New("snapshot: remove incomplete private evidence failed"),
					errors.New("snapshot: remove unpublished private evidence failed"),
					errors.New("snapshot: additional private evidence residue may remain; only one verified location can be returned"))
			}
			runner.snapshotWriter = writer
			original := run
			runner.captureSnapshot(task, request, nil, &run, "")
			require.NotEmpty(t, writer.published, "tuple injection follows actual publication, not an actual unlink fault")
			require.Equal(t, writer.published, run.SnapshotPath)
			original.SnapshotPath, original.Evidence = run.SnapshotPath, run.Evidence
			require.Equal(t, original, run, "best-effort publication must preserve the execution outcome")
			require.NoError(t, evidence.Validate(run.Evidence))
			data, err := os.ReadFile(run.SnapshotPath)
			require.NoError(t, err)
			var captured snapshot.Snapshot
			require.NoError(t, json.Unmarshal(data, &captured))
			require.Equal(t, task.TestID, captured.Task.TestID)
			require.Equal(t, run.RunNumber, captured.Task.RunNumber)
			if writer.problem != nil {
				require.Equal(t, "unavailable", run.Evidence.Artifacts[0].Availability)
				require.Equal(t, "capture_failed", run.Evidence.Diagnostics[0].Code)
				for _, artifact := range run.Evidence.Artifacts {
					require.Empty(t, artifact.Document)
					require.Empty(t, artifact.Pointer)
				}
				var logged struct {
					Message string `json:"msg"`
					Error   string `json:"err"`
				}
				require.NoError(t, json.Unmarshal(logs.Bytes(), &logged))
				require.Equal(t, "snapshot write failed", logged.Message)
				require.Equal(t, writer.problem.Error(), logged.Error)
			} else {
				require.Equal(t, captured.Evidence, run.Evidence)
				require.NotContains(t, logs.String(), "snapshot write failed")
			}
		})
	}
}

func TestSnapshotCallerNilWriterOptions(t *testing.T) {
	for _, name := range []string{"never-configured", "explicit-nil", "configured-then-nil"} {
		t.Run(name, func(t *testing.T) {
			cfg, task, request, run := snapshotCallerInputs(t)
			root := filepath.Join(t.TempDir(), "uncreated")
			var options []RunnerOption
			var disabled *snapshot.Writer
			switch name {
			case "explicit-nil":
				options = []RunnerOption{WithSnapshotWriter(disabled)}
			case "configured-then-nil":
				options = []RunnerOption{WithSnapshotWriter(snapshot.NewWriter(root)), WithSnapshotWriter(disabled)}
			}
			runner := NewEvalRunner(cfg, nil, options...)
			require.Nil(t, runner.snapshotWriter)
			require.Nil(t, runner.snapshotSkipDirs(task))
			original := run
			runner.captureSnapshot(task, request, nil, &run, "")
			require.Equal(t, original, run)
			_, err := os.Stat(root)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
