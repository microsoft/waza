package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

type attemptCleanupEngine struct {
	execution.AgentEngine
	waitForDeadline bool
	problem         error
	calls           int
}

type attemptCleanupExecutionFailure struct{}

func (*attemptCleanupExecutionFailure) Error() string { return "original execution failure" }

func (e *attemptCleanupEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	response, err := e.AgentEngine.Execute(ctx, req)
	if e.waitForDeadline {
		<-ctx.Done()
	}
	return response, errors.Join(err, e.problem)
}

type attemptCleanupSnapshotWriter struct {
	*snapshot.Writer
	calls       int
	beforeWrite func()
}

func (w *attemptCleanupSnapshotWriter) Write(value *snapshot.Snapshot) (string, error) {
	w.calls++
	w.beforeWrite()
	return w.Writer.Write(value)
}

func TestAttemptCleanupRetainsTypedCausesAndArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cleanupFail bool
		executeFail bool
		deadline    bool
		controlled  bool
		gradedFail  bool
		canceled    bool
	}{
		{name: "success"},
		{name: "graded-failure-with-successful-cleanup", gradedFail: true},
		{name: "execution-failure-with-successful-cleanup", executeFail: true},
		{name: "cleanup-only", cleanupFail: true},
		{name: "execution-and-cleanup", executeFail: true, cleanupFail: true},
		{name: "execution-canceled-and-cleanup", executeFail: true, cleanupFail: true, canceled: true},
		{name: "legacy-deadline-and-cleanup", deadline: true, executeFail: true, cleanupFail: true},
		{name: "controlled-deadline-and-cleanup", deadline: true, controlled: true, executeFail: true, cleanupFail: true},
		{name: "controlled-deadline-before-engine-cancellation-and-cleanup", deadline: true, controlled: true, executeFail: true, cleanupFail: true, canceled: true},
		{name: "controlled-nil-error-deadline-and-cleanup", deadline: true, controlled: true, cleanupFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, mcp, tmp := newMCPLifetimeRunner(t)
			runner.skipGraders = false
			task := &models.TestCase{
				TestID: "attempt-cleanup", Stimulus: models.TaskStimulus{Message: "read"},
				Expectation: models.TaskExpectation{MustInclude: []string{"ordinal"}},
			}
			if tc.gradedFail {
				task.Expectation.MustInclude = []string{"deliberately absent output"}
			}
			seconds := 1
			task.TimeoutSec = &seconds
			executionCause := &attemptCleanupExecutionFailure{}
			engine := &attemptCleanupEngine{AgentEngine: mcp, waitForDeadline: tc.deadline}
			if tc.executeFail {
				engine.problem = fmt.Errorf("executing selected task: %w", executionCause)
				if tc.canceled {
					engine.problem = errors.Join(engine.problem, context.Canceled)
				}
			}
			runner.engine = engine
			var residue, attemptDir string
			mcp.afterExecute = func(configFile string) {
				attemptDir = filepath.Dir(filepath.Dir(configFile))
				if tc.cleanupFail {
					residue = filepath.Join(filepath.Dir(configFile), "unowned")
					require.NoError(t, os.WriteFile(residue, []byte("preserve unrelated entry"), 0600))
				}
			}
			writer := &attemptCleanupSnapshotWriter{Writer: snapshot.NewWriter(t.TempDir())}
			writer.beforeWrite = func() {
				require.NotEmpty(t, attemptDir, "the production execution must have created owned state")
				if tc.cleanupFail {
					body, err := os.ReadFile(residue)
					require.NoError(t, err)
					require.Equal(t, "preserve unrelated entry", string(body))
				} else {
					require.NoDirExists(t, attemptDir, "cleanup must precede artifact publication")
				}
			}
			runner.snapshotWriter = writer
			runner.evalRunID = "cleanup-eval"
			var result attemptResult
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				start := time.Now()
				result = runner.executeAttemptWithRequest(ctx, task, 2, 1, nil, tc.controlled)
				if tc.deadline {
					require.Equal(t, time.Second, time.Since(start))
				}
				require.NoError(t, ctx.Err(), "only the execution deadline, not the parent, should expire")
			})
			require.Equal(t, 1, engine.calls)
			require.Len(t, mcp.results, 1)
			require.Len(t, mcp.configs, 1)
			require.Equal(t, 1, writer.calls, "finalize and publish the actual attempt exactly once")
			run := result.run
			require.Equal(t, 2, run.RunNumber)
			require.Equal(t, 1, run.Attempts)
			require.Equal(t, "read", run.Prompt)
			require.Equal(t, mcp.response.FinalOutput, run.FinalOutput)
			require.Contains(t, run.FinalOutput, `{\"ordinal\":1}`)
			failed := tc.executeFail || tc.cleanupFail || tc.controlled && tc.deadline
			if failed {
				require.Equal(t, models.StatusError, run.Status)
				require.Error(t, result.cause)
			} else {
				require.NoError(t, result.cause, "a failed grader is not an execution or cleanup error")
				if tc.gradedFail {
					require.Equal(t, models.StatusFailed, run.Status)
				} else {
					require.Equal(t, models.StatusPassed, run.Status)
				}
				require.Len(t, run.Validations, 1)
				require.Equal(t, !tc.gradedFail, run.Validations["_output_contains"].Passed)
			}
			require.Equal(t, tc.executeFail, errors.Is(result.cause, executionCause))
			var typedCause *attemptCleanupExecutionFailure
			require.Equal(t, tc.executeFail, errors.As(result.cause, &typedCause))
			if tc.executeFail {
				require.Same(t, executionCause, typedCause)
			}
			require.Equal(t, tc.controlled && tc.deadline, errors.Is(result.cause, context.DeadlineExceeded),
				"legacy errors must not gain a deadline cause; controlled errors must retain the observed deadline")
			require.Equal(t, tc.canceled, errors.Is(result.cause, context.Canceled), "cleanup must not manufacture or erase cancellation")
			if tc.cleanupFail {
				require.False(t, mcp.response.Success)
				require.Equal(t, run.ErrorMsg, mcp.response.ErrorMsg)
				require.Contains(t, run.ErrorMsg, "unrecognized MCP mock cleanup entry")
				require.Contains(t, run.ErrorMsg, "if renamed, locate it before manual cleanup")
				require.Equal(t, 1, strings.Count(run.ErrorMsg, "clean up MCP mock attempt state:"))
				require.Equal(t, 1, strings.Count(result.cause.Error(), "clean up MCP mock attempt state:"))
				require.Contains(t, result.cause.Error(), residue)
				if tc.executeFail {
					require.True(t, strings.HasPrefix(run.ErrorMsg, engine.problem.Error()+"; clean up MCP mock attempt state:"))
				}
				moved := attemptDir + "-closed"
				require.NoError(t, os.Rename(attemptDir, moved), "cleanup must release the owned directory handles")
				body, err := os.ReadFile(filepath.Join(moved, filepath.Base(filepath.Dir(residue)), "unowned"))
				require.NoError(t, err)
				require.Equal(t, "preserve unrelated entry", string(body))
			} else {
				require.NoDirExists(t, attemptDir)
				if tc.executeFail {
					require.Equal(t, engine.problem.Error(), run.ErrorMsg, "keep historical serialized execution diagnostics")
				} else {
					require.Empty(t, run.ErrorMsg)
				}
				require.True(t, mcp.response.Success, "do not change the legacy engine response on execution error alone")
			}
			if failed {
				envelope := runnerterminal.Failure("execution", result.cause)
				expected := runnerterminal.InfrastructureFailure
				if tc.controlled && tc.deadline {
					expected = runnerterminal.Timeout
				} else if tc.canceled {
					expected = runnerterminal.Canceled
				}
				require.Equal(t, expected, envelope.Outcome)
			}
			body, err := os.ReadFile(run.SnapshotPath)
			require.NoError(t, err)
			var recorded snapshot.Snapshot
			require.NoError(t, json.Unmarshal(body, &recorded))
			require.Equal(t, run.Status, recorded.Result.Status)
			require.Equal(t, run.ErrorMsg, recorded.Result.ErrorMsg)
			require.Equal(t, run.FinalOutput, recorded.Result.FinalOutput)
			entries, err := os.ReadDir(filepath.Dir(run.SnapshotPath))
			require.NoError(t, err)
			require.Len(t, entries, 1, "one actual snapshot, not duplicate finalization")
			require.DirExists(t, tmp, "the invocation's parent must survive cleanup")
		})
	}
}

func TestInlineAttemptWithoutOwnedMCPCleanup(t *testing.T) {
	runner, task := controlledFixture(t)
	tmp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tmp)
	}
	engine := &attemptCleanupEngine{AgentEngine: runner.engine}
	runner.engine = engine
	input, err := runner.PrepareInlineInput(task)
	require.NoError(t, err)
	run, err := runner.ExecuteInlineAttempt(t.Context(), task, input, 1, 1)
	require.NoError(t, err)
	require.Equal(t, models.StatusPassed, run.Status)
	require.Empty(t, run.ErrorMsg)
	require.Equal(t, 1, engine.calls)
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the concrete mock runtime retains its workspace until shutdown")
	require.True(t, entries[0].IsDir())
	require.True(t, strings.HasPrefix(entries[0].Name(), "waza-mock-"),
		"prepared inline inputs must not manufacture owned MCP attempt state")
}
