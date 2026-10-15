package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type controlledResponseEngine struct {
	execution.AgentEngine
	response *execution.ExecutionResponse
	err      error
}

func (e controlledResponseEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return e.response, e.err
}

func TestControlledOutputUsesActualResponsePresence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *execution.ExecutionResponse
		err      error
		present  bool
	}{{"empty", &execution.ExecutionResponse{}, nil, true},
		{"text", &execution.ExecutionResponse{FinalOutput: "hello"}, nil, true},
		{"missing", nil, nil, false},
		{"error", &execution.ExecutionResponse{FinalOutput: "ignored"}, errors.New("synthetic execute error"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			r, task := controlledFixture(t)
			r.engine = controlledResponseEngine{AgentEngine: r.engine, response: tc.response, err: tc.err}
			var output *string
			r.controlledOutput = func(s string) { output = new(s) }
			run := r.executeRunWithAttempt(t.Context(), task, 1, 1, nil)
			require.Equal(t, tc.present, output != nil)
			if tc.present {
				require.Equal(t, tc.response.FinalOutput, *output)
				require.Equal(t, *output, run.FinalOutput)
			} else {
				require.Equal(t, models.StatusError, run.Status)
			}
		})
	}
}

func TestControlledOutputObserverRestoresAndDoesNotObserveLegacy(t *testing.T) {
	r, task := controlledFixture(t)
	calls := 0
	r.controlledOutput = func(string) { calls++ }
	observation, err := r.ExecuteControlledAttempt(t.Context(), task,
		models.EvidenceOrigin{EvalID: "fresh", TaskID: task.TestID, RunNumber: 1, AttemptCount: 1})
	require.NoError(t, err)
	require.NotNil(t, observation.Output)
	require.Equal(t, observation.Run.FinalOutput, *observation.Output)
	require.Zero(t, calls)
	r.controlledOutput("restored")
	require.Equal(t, 1, calls)
	r.executeRunWithAttempt(t.Context(), task, 2, 0, nil)
	require.Equal(t, 1, calls)
}
