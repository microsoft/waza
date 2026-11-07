package executionlifecycle

import (
	"bytes"
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
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

const origin = "2026-11-07T17:00:00Z"

func source(t *testing.T) ([]byte, runnerterminal.Envelope, runnerterminal.Binding) {
	t.Helper()
	request, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	envelope, binding := runnerunit.NewService(nil).RunTerminal(t.Context(), request)
	require.NotNil(t, binding)
	require.Equal(t, runnerterminal.Success, envelope.Outcome)
	require.Equal(t, 1, envelope.EngineCalls)
	require.Len(t, envelope.Observation.Validations, 1)
	return reservationFor(t, *binding), envelope, *binding
}

func reservationFor(t *testing.T, binding runnerterminal.Binding) []byte {
	t.Helper()
	declaration := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical:     executionattempt.LogicalFrom(*binding.Identity),
		ExecutionID: "exec-" + strings.Repeat("a", 64),
		Binding: artifactmanifest.Binding{
			Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode,
			GraderIDs: binding.GraderIDs,
		},
	}
	reservation, err := executionattempt.EncodeAttempt(declaration)
	require.NoError(t, err)
	return reservation
}

func event(p *Projection, kind, at string) Event {
	return Event{
		Kind: "waza.execution-lifecycle-observation", SchemaVersion: Version,
		ExecutionID: p.value.ExecutionID, ReservationDigest: p.value.ReservationDigest,
		Sequence: len(p.value.Observations) + 1, ObservedAt: at, Event: kind,
	}
}

func view(t *testing.T, p *Projection, at string) Snapshot {
	t.Helper()
	result, err := p.Snapshot(at, time.Minute)
	require.NoError(t, err)
	return result
}

func TestActualRunnerTerminalRetained(t *testing.T) {
	reservation, terminal, binding := source(t)
	original, err := runnerterminal.Encode(terminal, &binding)
	require.NoError(t, err)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "started", origin)))
	last := event(p, "terminal_recorded", origin)
	last.Terminal = &terminal
	require.NoError(t, p.Apply(last))
	result := view(t, p, "")
	require.Equal(t, "terminal-recorded", result.State)
	require.Equal(t, "caller-declared-unverified", result.Evidence)
	require.Equal(t, 1, *result.CompletedUnits)
	require.Equal(t, "passed", result.Terminal.ExecutionVerdict)
	require.Equal(t, artifactmanifest.Hash(original), result.Terminal.Digest)
	require.Equal(t, 0, result.Terminal.ExitCode)
	require.Nil(t, result.SuspectedOrphan)
	require.Equal(t, RetryAdvice{"none", "completed-success"}, result.Retry)
	require.Equal(t, []string{"inspect-original-evidence"}, result.Actions)
	require.ErrorIs(t, p.Apply(event(p, "heartbeat", origin)), ErrTransition)
	require.Equal(t, result, view(t, p, ""))
	after, err := runnerterminal.Encode(terminal, &binding)
	require.NoError(t, err)
	require.Equal(t, original, after, "projection must not mutate original terminal or binding")
}

func TestCancellationAndStopAreNotTerminalEvidence(t *testing.T) {
	reservation, _, binding := source(t)
	p, err := New(reservation, binding, origin)
	require.NoError(t, err)
	for _, kind := range []string{"cancellation_acknowledged", "force_requested", "stop_observed"} {
		bad := event(p, kind, origin)
		if kind == "cancellation_acknowledged" {
			bad.Handling = "sdk-abort-returned"
		}
		if kind == "stop_observed" {
			bad.Stop = "process-wait-returned"
		}
		before := view(t, p, "")
		require.ErrorIs(t, p.Apply(bad), ErrTransition)
		require.Equal(t, before, view(t, p, ""))
	}
	require.NoError(t, p.Apply(event(p, "started", origin)))
	require.NoError(t, p.Apply(event(p, "cancellation_requested", origin)))
	ack := event(p, "cancellation_acknowledged", origin)
	ack.Handling = "sdk-abort-returned"
	require.NoError(t, p.Apply(ack))
	require.Equal(t, "handling_acknowledged", view(t, p, "").Cancellation)
	require.Nil(t, view(t, p, "").Terminal)
	require.NoError(t, p.Apply(event(p, "force_requested", origin)))
	ack = event(p, "cancellation_acknowledged", origin)
	ack.Handling = "context-cancellation-observed"
	require.NoError(t, p.Apply(ack))
	require.Equal(t, "force_requested", view(t, p, "").Cancellation)
	stop := event(p, "stop_observed", origin)
	stop.Stop = "process-wait-returned"
	require.NoError(t, p.Apply(stop))
	result := view(t, p, "")
	require.Equal(t, "stopped-without-terminal", result.State)
	require.Nil(t, result.Terminal)
	require.Nil(t, result.CompletedUnits, "process stop does not establish completed work")
	require.Equal(t, "unknown", result.Retry.Advice)
	require.ErrorIs(t, p.Apply(event(p, "heartbeat", origin)), ErrTransition)
}

func TestHeartbeatUnknownBoundaryAndTerminalControls(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "started", origin)))
	require.Nil(t, view(t, p, "2026-11-07T18:00:00Z").SuspectedOrphan, "missing heartbeat remains unknown")
	require.NoError(t, p.Apply(event(p, "heartbeat", origin)))
	require.Nil(t, view(t, p, "").SuspectedOrphan, "missing as-of remains unknown")
	now := view(t, p, origin)
	require.Equal(t, int64(0), *now.HeartbeatAgeMS)
	require.False(t, *now.SuspectedOrphan)
	require.False(t, *view(t, p, "2026-11-07T17:00:59.999999999Z").SuspectedOrphan)
	stale := view(t, p, "2026-11-07T17:01:00Z")
	require.True(t, *stale.SuspectedOrphan)
	require.Contains(t, stale.Actions, "investigate-worker-no-reclaim")
	require.NotContains(t, stale.Actions, "retry")
	require.Equal(t, "running", stale.State)
	done := event(p, "terminal_recorded", "2026-11-07T17:01:00Z")
	done.Terminal = &terminal
	require.NoError(t, p.Apply(done))
	require.False(t, *view(t, p, "2026-11-07T18:00:00Z").SuspectedOrphan)
}

func TestDeadlineDoesNotReplaceOriginalTerminal(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "2026-11-07T17:01:00Z")
	require.NoError(t, err)
	before := view(t, p, "")
	require.ErrorIs(t, p.Apply(event(p, "deadline_expired", origin)), ErrTransition)
	require.Equal(t, before, view(t, p, ""))
	require.NoError(t, p.Apply(event(p, "deadline_expired", "2026-11-07T17:01:00Z")))
	done := event(p, "terminal_recorded", "2026-11-07T17:01:00Z")
	done.Terminal = &terminal
	require.NoError(t, p.Apply(done))
	result := view(t, p, "")
	require.True(t, result.DeadlineExpired)
	require.Equal(t, runnerterminal.Success, result.Terminal.Outcome)
	require.Contains(t, result.Diagnostics, "deadline-observation-differs-from-terminal-outcome")
	require.Equal(t, 0, result.Terminal.ExitCode)
}

func TestTerminalAdviceAndPartialCompletion(t *testing.T) {
	for _, cause := range []struct {
		name    string
		outcome runnerterminal.Outcome
		err     error
		advice  string
	}{
		{"infrastructure", runnerterminal.InfrastructureFailure, errors.New("runtime unavailable"), "conditional-fresh-attempt"},
		{"canceled", runnerterminal.Canceled, context.Canceled, "conditional-fresh-attempt"},
		{"timeout", runnerterminal.Timeout, context.DeadlineExceeded, "conditional-fresh-attempt"},
	} {
		t.Run(cause.name, func(t *testing.T) {
			reservation, _, binding := source(t)
			p, err := New(reservation, binding, "")
			require.NoError(t, err)
			failed := runnerterminal.Failure("startup", cause.err)
			failed.Validation, failed.Identity, failed.Selection = "valid", binding.Identity, &binding.Selection
			terminal := event(p, "terminal_recorded", origin)
			terminal.Terminal = &failed
			require.NoError(t, p.Apply(terminal))
			result := view(t, p, "")
			require.Equal(t, cause.outcome, result.Terminal.Outcome)
			require.Nil(t, result.CompletedUnits, "absent observation cannot invent progress")
			require.Equal(t, cause.advice, result.Retry.Advice)
			require.Contains(t, result.Actions, "review-independent-fresh-attempt")
		})
	}
	reservation, terminal, binding := source(t)
	for _, outcome := range []runnerterminal.Outcome{runnerterminal.EvaluationFailure, runnerterminal.InfrastructureFailure} {
		p, err := New(reservation, binding, "")
		require.NoError(t, err)
		terminal.Observation.Status, terminal.Observation.Validations[0].Passed = "failed", false
		terminal.Outcome = outcome
		terminal.ExitCode, _ = outcome.ExitCode()
		terminal.Diagnostics = []runnerterminal.Diagnostic{{Code: "failed_checks", Message: "Observed check failed."}}
		if outcome == runnerterminal.InfrastructureFailure {
			terminal.FailureBoundary = "shutdown"
		}
		done := event(p, "terminal_recorded", origin)
		done.Terminal = &terminal
		require.NoError(t, p.Apply(done))
		require.Equal(t, 1, *view(t, p, "").CompletedUnits)
		require.Equal(t, "failed", view(t, p, "").Terminal.ExecutionVerdict)
	}
	for _, outcome := range []runnerterminal.Outcome{runnerterminal.InvalidInput, runnerterminal.UnsupportedCapability} {
		advice, err := retryAdvice(outcome)
		require.NoError(t, err)
		require.Equal(t, "none", advice.Advice)
	}
	_, err := retryAdvice("future-unknown")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestInvalidObservationsPreserveProjection(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "started", origin)))
	for _, change := range []func(*Event){
		func(e *Event) { e.Sequence++ },
		func(e *Event) { e.ObservedAt = "2026-11-07T16:59:59Z" },
		func(e *Event) { e.ExecutionID = "exec-" + strings.Repeat("b", 64) },
		func(e *Event) { e.ReservationDigest = strings.Repeat("b", 64) },
		func(e *Event) { e.Event = "started" },
		func(e *Event) { e.Event = "deadline_expired" },
	} {
		e := event(p, "heartbeat", origin)
		change(&e)
		before := view(t, p, "")
		require.Error(t, p.Apply(e))
		require.Equal(t, before, view(t, p, ""))
	}
	terminal.Identity.Attempt = "attempt-" + strings.Repeat("b", 64)
	done := event(p, "terminal_recorded", origin)
	done.Terminal = &terminal
	require.ErrorIs(t, p.Apply(done), ErrContext)
	require.Nil(t, view(t, p, "").Terminal)
}

func TestBoundedEventsAndDefensiveCopies(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	binding.GraderIDs[0] = "modified-after-new"
	require.NoError(t, p.Apply(event(p, "started", origin)))
	for range MaxEvents - 2 {
		require.NoError(t, p.Apply(event(p, "heartbeat", origin)))
	}
	require.Len(t, view(t, p, "").Observations, MaxEvents-1)
	before := view(t, p, "")
	require.Error(t, p.Apply(event(p, "heartbeat", origin)))
	require.Equal(t, before, view(t, p, ""))
	before.Observations[0].Event = "modified"
	*before.CompletedUnits = 100
	require.Equal(t, "started", view(t, p, "").Observations[0].Event)
	require.Equal(t, 0, *view(t, p, "").CompletedUnits)
	done := event(p, "terminal_recorded", origin)
	done.Terminal = &terminal
	require.NoError(t, p.Apply(done), "heartbeats cannot exhaust terminal evidence slot")
	require.Len(t, view(t, p, "").Observations, MaxEvents)
	_, err = p.Snapshot(origin, 0)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = p.Snapshot("2026-11-07T16:59:59Z", time.Minute)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestStrictWireAndCanonicalEncoding(t *testing.T) {
	reservation, _, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	base, err := Encode(event(p, "started", origin))
	require.NoError(t, err)
	parsed, err := Parse(append(append([]byte(" \r\n"), base...), '\n'))
	require.NoError(t, err)
	canonical, err := Encode(*parsed)
	require.NoError(t, err)
	require.Equal(t, base, canonical)
	var object map[string]any
	require.NoError(t, json.Unmarshal(base, &object))
	for key, old := range object {
		delete(object, key)
		data, err := json.Marshal(object)
		require.NoError(t, err)
		_, err = Parse(data)
		require.ErrorIs(t, err, ErrInvalid, "missing %s", key)
		object[key] = nil
		data, err = json.Marshal(object)
		require.NoError(t, err)
		_, err = Parse(data)
		require.ErrorIs(t, err, ErrInvalid, "null %s", key)
		object[key] = old
	}
	for _, mutation := range []string{
		strings.Replace(string(base), `"kind":`, `"k\u0069nd":"wrong","kind":`, 1),
		strings.Replace(string(base), `"sequence":1`, `"sequence":1.5`, 1),
		strings.Replace(string(base), origin, "2026-11-07T17:00:00.000Z", 1),
		strings.Replace(string(base), origin, "0000-01-01T00:00:00Z", 1),
		strings.Replace(string(base), origin, "2026-02-30T00:00:00Z", 1),
		strings.Replace(string(base), `"started"`, `"\ud800"`, 1),
		string(base) + "{}",
	} {
		require.NotEqual(t, string(base), mutation)
		_, err := Parse([]byte(mutation))
		require.ErrorIs(t, err, ErrInvalid)
	}

	_, err = Parse(bytes.Repeat([]byte(" "), MaxBytes+1))
	require.ErrorIs(t, err, ErrInvalid)
	bad := event(p, "started", origin)
	bad.Event = string([]byte{0xff})
	_, err = Encode(bad)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestOriginalContextAndTimestampAdmission(t *testing.T) {
	reservation, _, binding := source(t)
	for _, change := range []func(*runnerterminal.Binding){
		func(b *runnerterminal.Binding) { b.Identity = nil },
		func(b *runnerterminal.Binding) { b.GraderIDs = nil },
		func(b *runnerterminal.Binding) { b.GraderIDs = []string{} },
		func(b *runnerterminal.Binding) { b.ExecutionMode = "copilot" },
		func(b *runnerterminal.Binding) { b.Selection.Attempt++ },
		func(b *runnerterminal.Binding) {
			identity := *b.Identity
			identity.Attempt = "attempt-" + strings.Repeat("b", 64)
			b.Identity = &identity
		},
	} {
		original := binding
		change(&original)
		_, err := New(reservation, original, "")
		require.ErrorIs(t, err, ErrContext)
	}
	_, err := New(append(bytes.Clone(reservation), '\n'), binding, "")
	require.ErrorIs(t, err, ErrContext)
	_, err = New([]byte("{}"), binding, "")
	require.ErrorIs(t, err, ErrContext)
	for _, invalid := range []string{
		"2026-11-07T17:00:00+00:00", "2026-11-07T17:00:00.000Z", "0000-01-01T00:00:00Z",
		"2026-02-30T00:00:00Z",
	} {
		_, err := New(reservation, binding, invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "started", "0001-01-01T00:00:00Z")))
	require.NoError(t, p.Apply(event(p, "heartbeat", "0001-01-01T00:00:00Z")))
	_, err = p.Snapshot("9999-01-01T00:00:00Z", time.Minute)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestStoppedWithoutTerminalCanRetainLaterOriginalTerminal(t *testing.T) {
	reservation, terminal, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	require.NoError(t, p.Apply(event(p, "cancellation_requested", origin)))
	require.NoError(t, p.Apply(event(p, "force_requested", origin)))
	stopped := event(p, "stop_observed", origin)
	stopped.Stop = "process-wait-returned"
	require.NoError(t, p.Apply(stopped))
	require.Nil(t, view(t, p, "").CompletedUnits)
	done := event(p, "terminal_recorded", origin)
	done.Terminal = &terminal
	require.NoError(t, p.Apply(done))
	result := view(t, p, "")
	require.Equal(t, "stop_observed", result.Cancellation)
	require.Equal(t, runnerterminal.Success, result.Terminal.Outcome)
	require.Equal(t, 1, *result.CompletedUnits)
	terminal.Observation.Status = "error"
	require.Equal(t, "passed", view(t, p, "").Terminal.ExecutionVerdict, "retained projection must not alias caller terminal")
	*result.CompletedUnits = 9
	result.Terminal.Outcome = runnerterminal.Timeout
	require.Equal(t, runnerterminal.Success, view(t, p, "").Terminal.Outcome)
	require.Equal(t, 1, *view(t, p, "").CompletedUnits)
}

func TestProductionTerminalOutcomesThroughProjection(t *testing.T) {
	for _, control := range []struct {
		name    string
		outcome runnerterminal.Outcome
		units   *int
	}{
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
			p, err := New(reservationFor(t, *binding), *binding, "")
			require.NoError(t, err)
			done := event(p, "terminal_recorded", origin)
			done.Terminal = &terminal
			require.NoError(t, p.Apply(done))
			result := view(t, p, "")
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
