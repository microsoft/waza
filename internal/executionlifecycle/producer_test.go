package executionlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestProductionTerminalOutcomesThroughProjection(t *testing.T) {
	for _, control := range []struct {
		name    string
		outcome runnerterminal.Outcome
		units   *int
	}{
		{"success", runnerterminal.Success, new(1)},
		{"failed-check", runnerterminal.EvaluationFailure, new(1)},
		{"factory-failure", runnerterminal.InfrastructureFailure, nil},
		{"cancellation", runnerterminal.Canceled, nil},
		{"deadline", runnerterminal.Timeout, nil},
	} {
		t.Run(control.name, func(t *testing.T) {
			data, err := os.ReadFile("../runnercontract/testdata/request.json")
			require.NoError(t, err)
			request, err := runnercontract.Parse(data)
			require.NoError(t, err)
			ctx := t.Context()
			service := runnerunit.NewService(nil)
			switch control.name {
			case "failed-check":
				old := request.Input.Task
				request.Input.Task = strings.Replace(old, "- hello\n", "- output-never-present\n", 1)
				require.NotEqual(t, old, request.Input.Task)
			case "factory-failure":
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, errors.New("controlled factory failure")
				})
			case "cancellation":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "deadline":
				expired, cancel := context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
				ctx = expired
			}
			data, err = json.Marshal(request)
			require.NoError(t, err)
			terminal, binding := service.RunTerminal(ctx, data)
			require.NotNil(t, binding)
			require.Equal(t, control.outcome, terminal.Outcome)
			original, err := runnerterminal.Encode(terminal, binding)
			require.NoError(t, err)
			attempt := executionattempt.Attempt{
				Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
				Logical:     executionattempt.LogicalFrom(*binding.Identity),
				ExecutionID: "exec-" + strings.Repeat("a", 64),
				Binding: artifactmanifest.Binding{
					Identity: *binding.Identity, Selection: binding.Selection,
					ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
				},
			}
			reservation, err := executionattempt.EncodeAttempt(attempt)
			require.NoError(t, err)
			p, err := executionlifecycle.New(reservation, *binding, "")
			require.NoError(t, err)
			done := executionlifecycle.Event{
				Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
				ExecutionID: attempt.ExecutionID, ReservationDigest: artifactmanifest.Hash(reservation),
				Sequence: 1, ObservedAt: "2026-11-07T17:00:00Z", Event: "terminal_recorded", Terminal: &terminal,
			}
			require.NoError(t, p.Apply(done))
			result, err := p.Snapshot("", time.Minute)
			require.NoError(t, err)
			require.Equal(t, control.outcome, result.Terminal.Outcome)
			require.Equal(t, terminal.ExitCode, result.Terminal.ExitCode)
			require.Equal(t, control.units, result.CompletedUnits)
			require.Equal(t, artifactmanifest.Hash(original), result.Terminal.Digest)
			after, err := runnerterminal.Encode(terminal, binding)
			require.NoError(t, err)
			require.Equal(t, original, after)
		})
	}
}
