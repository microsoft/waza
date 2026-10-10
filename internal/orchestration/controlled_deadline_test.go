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
	for _, attempt := range []int{0, 1} {
		t.Run(map[int]string{0: "legacy_unchanged", 1: "controlled_operational_error"}[attempt], func(t *testing.T) {
			t.Parallel()
			runner, task := controlledFixture(t)
			runner.engine = deadlineIgnoringEngine{AgentEngine: runner.engine}
			seconds := 1
			task.TimeoutSec = &seconds
			run := runner.executeRunWithAttempt(t.Context(), task, 1, attempt, nil)
			if attempt == 0 {
				if run.Status != models.StatusPassed {
					t.Fatalf("legacy engine semantics changed: %+v", run)
				}
			} else if run.Status != models.StatusError || !strings.Contains(run.ErrorMsg, "deadline") {
				t.Fatalf("expired controlled execution counted as behavioral pass: %+v", run)
			}
		})
	}
}
