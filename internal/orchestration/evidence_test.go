package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestEvidenceRetryAndCacheKeepActualSourceAttempt(t *testing.T) {
	calls := 0
	engine := &sessionAccountingEngine{execute: func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		calls++
		output := "bad"
		if calls == 2 {
			output = "good"
		}
		return &execution.ExecutionResponse{Success: true, FinalOutput: output}, nil
	}}
	spec := &models.EvalSpec{Config: models.Config{EngineType: "mock", TrialsPerTask: 1, TimeoutSec: 30, MaxAttempts: 2}}
	task := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "answer"},
		Validators: []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindText,
			Parameters: models.TextGraderParameters{Contains: []string{"good"}}}},
		Requirements: []models.Requirement{{ID: "req", Checks: []models.RequirementCheck{{Scope: "task", Grader: "check"}}}},
	}
	destination := t.TempDir()
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine,
		WithCache(cache.New(t.TempDir())), WithSnapshotWriter(snapshot.NewWriter(destination)))
	runner.evalRunID = "source-eval"
	first, hit := runner.runTest(t.Context(), task, 1, 1)
	require.False(t, hit)
	require.Equal(t, 2, calls)
	require.Len(t, first.Runs, 1)
	run := first.Runs[0]
	require.Equal(t, models.StatusPassed, run.Status)
	require.Equal(t, 2, run.Attempts)
	require.Equal(t, 2, run.Evidence.Origin.AttemptCount)
	require.Equal(t, "not_preserved", run.Evidence.Origin.PriorAttempts)
	require.Equal(t, "unknown", run.Evidence.Runtime.ExecutionMode, "a test engine is not a concrete MockEngine")
	require.NoError(t, evidence.Bind(run.Evidence, "source-eval", "task", 1, 2, false))
	loaded, err := snapshot.LoadSnapshotFile(run.SnapshotPath)
	require.NoError(t, err)
	require.Equal(t, run.Evidence.SHA256, loaded.Evidence.SHA256)
	require.Equal(t, "good", loaded.Result.FinalOutput)
	beforeBytes, err := os.ReadFile(run.SnapshotPath)
	require.NoError(t, err)
	files, err := os.ReadDir(destination)
	require.NoError(t, err)
	runner.evalRunID = "new-eval"
	second, hit := runner.runTest(t.Context(), task, 1, 1)
	require.True(t, hit)
	require.True(t, second.Cached)
	require.Equal(t, 2, calls)
	require.Equal(t, run.Evidence, second.Runs[0].Evidence)
	require.Equal(t, run.RequirementExplanations, second.Runs[0].RequirementExplanations)
	require.NoError(t, evidence.Bind(second.Runs[0].Evidence, "new-eval", "task", 1, 2, true))
	after, err := os.ReadDir(destination)
	require.NoError(t, err)
	require.Equal(t, len(files), len(after), "a cache hit must not publish a new snapshot")
	afterBytes, err := os.ReadFile(run.SnapshotPath)
	require.NoError(t, err)
	require.Equal(t, beforeBytes, afterBytes, "a cache hit must not overwrite its source snapshot")
}

func TestCancelledExecutionEvidencePreservesPartialOutputWithoutSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	engine := &sessionAccountingEngine{execute: func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		cancel()
		return &execution.ExecutionResponse{FinalOutput: "recorded before cancellation"}, context.Canceled
	}}
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30}}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine,
		WithSnapshotWriter(snapshot.NewWriter(t.TempDir())))
	runner.evalRunID = "eval"
	task := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "answer"},
		Requirements: []models.Requirement{{ID: "req", Checks: []models.RequirementCheck{{Scope: "task", Grader: "check"}}}},
		Validators:   []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindText}},
	}
	run := runner.executeAttempt(ctx, task, 1, 2)
	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, context.Canceled.Error())
	require.Equal(t, "recorded before cancellation", run.FinalOutput)
	require.NoError(t, evidence.Validate(run.Evidence))
	require.Equal(t, "execution", run.Evidence.Diagnostics[0].Category)
	reference, err := evidence.Reference(run.Evidence, "result")
	require.NoError(t, err)
	artifact, err := evidence.Resolve(run.Evidence, reference)
	require.NoError(t, err)
	require.Equal(t, "partial", artifact.Completeness)
	require.ErrorContains(t, evidence.VerifyContent(run.Evidence, reference, nil, true, false), "incomplete")
	require.Equal(t, "unresolved", run.RequirementExplanations[0].Checks[0].Observation)
	require.Equal(t, "operational_error", run.RequirementExplanations[0].Checks[0].Category)
	loaded, err := snapshot.LoadSnapshotFile(run.SnapshotPath)
	require.NoError(t, err)
	require.Equal(t, run.FinalOutput, loaded.Result.FinalOutput)
}

func TestEvidenceCaptureRemainsOptInWithoutSnapshotWriter(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30}}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), &trackingEngine{}, WithSkipGraders())
	run := runner.executeRun(t.Context(), &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "answer"}}, 1)
	require.Equal(t, models.StatusSkipped, run.Status)
	require.Nil(t, run.Evidence)
	require.Empty(t, run.SnapshotPath)
	require.Empty(t, run.RequirementExplanations)
}

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
