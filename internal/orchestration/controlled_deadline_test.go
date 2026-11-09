package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type legacyDeadlineErrorEngine struct {
	execution.AgentEngine
	err   error
	calls int
}

func (engine *legacyDeadlineErrorEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	engine.calls++
	<-ctx.Done()
	return &execution.ExecutionResponse{Success: true, FinalOutput: "hello"}, engine.err
}

func TestLegacyExecutionDeadlinePreservesObservation(t *testing.T) {
	for _, cause := range []error{errors.New("runtime unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			runner, task := controlledFixture(t)
			seconds := 1
			task.TimeoutSec = &seconds
			engineErr := fmt.Errorf("legacy engine interrupted: %w", cause)
			engine := &legacyDeadlineErrorEngine{AgentEngine: runner.engine, err: engineErr}
			runner.engine = engine
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				start := time.Now()
				run := runner.executeAttempt(ctx, task, 2, 1)
				require.Equal(t, time.Second, time.Since(start))
				require.NoError(t, ctx.Err())
				require.Equal(t, 1, engine.calls)
				require.Equal(t, models.StatusError, run.Status)
				require.Equal(t, engineErr.Error(), run.ErrorMsg)
				require.Equal(t, "hello", run.FinalOutput)
				require.Equal(t, task.Stimulus.Message, run.Prompt)
				require.Equal(t, 2, run.RunNumber)
				require.Equal(t, 1, run.Attempts)
				require.Empty(t, run.Validations)
			})
		})
	}
}

type deadlineIgnoringEngine struct {
	execution.AgentEngine
}

func (engine deadlineIgnoringEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	<-ctx.Done()
	return &execution.ExecutionResponse{FinalOutput: "hello"}, nil
}

func TestControlledExecutionRejectsNilErrorAfterDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempt  int
		ordinary bool
	}{
		{name: "legacy_unchanged"},
		{name: "ordinary_actual_attempt", attempt: 1, ordinary: true},
		{name: "controlled_operational_error", attempt: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner, task := controlledFixture(t)
			runner.engine = deadlineIgnoringEngine{AgentEngine: runner.engine}
			seconds := 1
			task.TimeoutSec = &seconds
			var run models.RunResult
			if tc.ordinary {
				run = runner.executeAttempt(t.Context(), task, 1, tc.attempt)
				if run.Attempts != tc.attempt {
					t.Fatalf("ordinary actual attempt lost: got %d, want %d", run.Attempts, tc.attempt)
				}
			} else {
				run = runner.executeRunWithAttempt(t.Context(), task, 1, tc.attempt, nil)
			}
			if tc.attempt == 0 || tc.ordinary {
				if run.Status != models.StatusPassed {
					t.Fatalf("legacy engine semantics changed: %+v", run)
				}
			} else if run.Status != models.StatusError || !strings.Contains(run.ErrorMsg, "deadline") {
				t.Fatalf("expired controlled execution counted as behavioral pass: %+v", run)
			}
		})
	}
}
