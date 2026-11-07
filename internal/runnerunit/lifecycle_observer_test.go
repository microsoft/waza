package runnerunit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func lifecycleClock() func() time.Time {
	at := time.Date(2026, 11, 7, 17, 0, 0, 0, time.UTC)
	return func() time.Time {
		value := at
		at = at.Add(time.Second)
		return value
	}
}

func TestLifecycleObserverActualSubmissionAndReuse(t *testing.T) {
	for _, name := range []string{"success", "factory-failure", "canceled-after-reservation"} {
		t.Run(name, func(t *testing.T) {
			backend := submissionBackend(t)
			observer, err := NewLifecycleObserver(backend, lifecycleClock())
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			service := NewService(func(config models.Config) (execution.AgentEngine, error) {
				capture, err := observer.RuntimeObservations()
				require.NoError(t, err, "actual post-reservation factory can select only this invocation's sink")
				require.NotNil(t, capture)
				calls++
				if name == "factory-failure" {
					return nil, errors.New("controlled lifecycle factory failure")
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			hooks := observer.Hooks()
			reserved := hooks.Reserved
			var deadline time.Time
			hooks.Reserved = func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, reservation executionledger.Reservation) error {
				var present bool
				deadline, present = ctx.Deadline()
				require.True(t, present)
				if err := reserved(ctx, ledger, creator, reservation); err != nil {
					return err
				}
				reservation.Bytes[0] = 'X'
				reservation.Attempt.Binding.GraderIDs[0] = "detached-callback-mutation"
				if name == "canceled-after-reservation" {
					cancel()
				}
				return nil
			}
			request := fixture(t)
			result, err := service.Submit(ctx, submissionWire(t, request, nil), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, "completed", result.Status)
			require.NotNil(t, result.Completion)
			outcome := runnerterminal.Success
			if name == "factory-failure" {
				outcome = runnerterminal.InfrastructureFailure
			}
			if name == "canceled-after-reservation" {
				outcome = runnerterminal.Canceled
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			require.Equal(t, outcome, result.Terminal.Outcome)
			_, err = observer.RuntimeObservations()
			require.ErrorIs(t, err, executionledger.ErrCapability, "joined terminal seals the original sink")
			require.NotNil(t, observer.native)
			require.False(t, observer.native.OwnedEngineBound, "mock/zero-factory outcomes do not invent native execution")
			require.Empty(t, observer.native.Observations)
			require.Equal(t, "unknown", observer.native.ProcessExit)
			inspection, err := observer.Inspection(t.Context(), "2026-11-07T17:01:00Z", 60000)
			require.NoError(t, err)
			require.Equal(t, deadline.UTC().Format(time.RFC3339Nano), inspection.Deadline)
			require.Len(t, inspection.ObservationDigests, 2)
			require.Equal(t, result.Reservation.Binding, inspection.OriginalAdmission)
			data, err := json.Marshal(inspection)
			require.NoError(t, err)
			snapshot, err := executionlifecycle.Inspect(t.Context(), backend, data)
			require.NoError(t, err)
			require.Equal(t, "caller-declared-unverified", snapshot.Evidence)
			require.Equal(t, "terminal-recorded", snapshot.State)
			require.Equal(t, outcome, snapshot.Terminal.Outcome)
			require.Equal(t, result.ExitCode, snapshot.Terminal.ExitCode)
			require.Equal(t, "none", snapshot.Cancellation, "joined terminal is not concrete cancellation handling")
			require.Empty(t, snapshot.Heartbeat, "callbacks do not invent worker heartbeats")
			require.False(t, snapshot.DeadlineExpired, "a timeout outcome is not a separately observed expiry")
			original, err := runnerterminal.Encode(*result.Terminal, result.Reservation.Binding.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, artifactmanifest.Hash(original), snapshot.Terminal.Digest)
			inspection.OriginalAdmission.GraderIDs[0] = "caller-mutated"
			inspection.ObservationDigests[0] = strings.Repeat("b", 64)
			retained, err := observer.Inspection(t.Context(), "", 60000)
			require.NoError(t, err)
			require.Equal(t, result.Reservation.Binding, retained.OriginalAdmission)
			require.NotEqual(t, inspection.ObservationDigests, retained.ObservationDigests)

			reuseObserver, err := NewLifecycleObserver(backend, lifecycleClock())
			require.NoError(t, err)
			_, err = reuseObserver.RuntimeObservations()
			require.ErrorIs(t, err, executionledger.ErrCapability, "no sink is exposed before an actual fresh reservation")
			request.Selection.Attempt++
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID,
				ManifestDigest: result.Completion.ManifestDigest}
			reused, err := service.Submit(t.Context(), submissionWire(t, request,
				&SubmissionPolicy{Mode: "reuse", Source: &source}), backend, reuseObserver.Hooks())
			require.NoError(t, err)
			require.Equal(t, "reused", reused.Status)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.Report, reused.Report)
			require.Equal(t, result.Terminal, reused.Terminal)
			_, err = reuseObserver.Inspection(t.Context(), "", 60000)
			require.ErrorIs(t, err, executionledger.ErrCapability, "reuse creates no new observed invocation")
		})
	}
}

type lifecyclePublicationFailure struct {
	*artifactmanifest.Local
	event string
	err   error
}

func (b *lifecyclePublicationFailure) Create(ctx context.Context, key string, data []byte) (bool, error) {
	var event struct {
		Kind  string `json:"kind"`
		Event string `json:"event"`
	}
	if json.Unmarshal(data, &event) == nil && event.Kind == "waza.execution-lifecycle-observation" && event.Event == b.event {
		return false, b.err
	}
	return b.Local.Create(ctx, key, data)
}

func TestLifecycleObserverPublicationFailurePreventsCompletion(t *testing.T) {
	for _, event := range []string{"started", "terminal_recorded"} {
		t.Run(event, func(t *testing.T) {
			failure := errors.New("controlled lifecycle publication failure")
			backend := &lifecyclePublicationFailure{Local: submissionBackend(t), event: event, err: failure}
			observer, err := NewLifecycleObserver(backend, lifecycleClock())
			require.NoError(t, err)
			result, err := NewService(nil).Submit(t.Context(), submissionWire(t, fixture(t), nil), backend.Local, observer.Hooks())
			require.ErrorIs(t, err, failure)
			require.ErrorContains(t, err, "at blob-", "ambiguous residue has a locatable digest, not authority")
			require.Equal(t, "submission_error", result.Status)
			require.Equal(t, 2, result.ExitCode)
			require.Nil(t, result.Completion)
			require.NotNil(t, result.Reservation)
			if event == "started" {
				require.Zero(t, result.EngineCalls)
			} else {
				require.Equal(t, runnerterminal.Success, result.Terminal.Outcome)
			}
			_, err = observer.Inspection(t.Context(), "", 60000)
			require.ErrorIs(t, err, failure)
			ledger, err := executionledger.New(backend.Local)
			require.NoError(t, err)
			_, err = ledger.ReadCompletion(t.Context(), result.Reservation.ExecutionID)
			require.ErrorIs(t, err, artifactmanifest.ErrMissing)
		})
	}
}

func TestLifecycleObserverRejectsMissingContextAndClockReordering(t *testing.T) {
	backend := submissionBackend(t)
	_, err := NewLifecycleObserver(nil, lifecycleClock())
	require.Error(t, err)
	_, err = NewLifecycleObserver(backend, nil)
	require.Error(t, err)
	observer, err := NewLifecycleObserver(backend, func() time.Time { return time.Time{} })
	require.NoError(t, err)
	result, err := NewService(nil).Submit(t.Context(), submissionWire(t, fixture(t), nil), backend, observer.Hooks())
	require.ErrorIs(t, err, executionlifecycle.ErrInvalid)
	require.Zero(t, result.EngineCalls)
	require.Nil(t, result.Completion)

	at := time.Date(2026, 11, 7, 17, 0, 0, 0, time.UTC)
	observer, err = NewLifecycleObserver(backend, func() time.Time {
		value := at
		at = at.Add(-time.Second)
		return value
	})
	require.NoError(t, err)
	result, err = NewService(nil).Submit(t.Context(), submissionWire(t, fixture(t), nil), backend, observer.Hooks())
	require.ErrorIs(t, err, executionlifecycle.ErrTransition)
	require.Equal(t, runnerterminal.Success, result.Terminal.Outcome)
	require.Nil(t, result.Completion)
	_, err = observer.Inspection(t.Context(), "", 60000)
	require.ErrorIs(t, err, executionlifecycle.ErrTransition)
}
