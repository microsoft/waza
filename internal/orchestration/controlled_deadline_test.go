package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

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
