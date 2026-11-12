package executionlifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
)

var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func testScope(t *testing.T) (*artifactmanifest.Diagnostics, artifactmanifest.Scope) {
	t.Helper()
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	store, err := NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := Scope("exec-"+strings.Repeat("a", 64), strings.Repeat("b", 64), "decision-"+strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	return store, scope
}

func at(offset time.Duration) time.Time { return epoch.Add(offset) }

func started(interval time.Duration, deadline time.Time) Event {
	return Event{Event: Started, At: epoch, DeadlineAt: &deadline, HeartbeatIntervalMS: interval.Milliseconds()}
}

func TestEventCodecIsCanonicalAndConditional(t *testing.T) {
	data, err := EncodeEvent(Event{Event: CancelRequested, At: epoch.In(time.FixedZone("x", 3600)), Reason: ReasonCaller})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"at":"2026-01-02T03:04:05Z"`) {
		t.Fatalf("event time was not normalized to UTC: %s", data)
	}
	if _, err := ParseEvent(data); err != nil {
		t.Fatal(err)
	}
	for name, event := range map[string]Event{
		"started without deadline": {Event: Started, At: epoch, HeartbeatIntervalMS: 1},
		"request without reason":   {Event: CancelRequested, At: epoch},
		"finished without outcome": {Event: Finished, At: epoch},
	} {
		if _, err := EncodeEvent(event); !errors.Is(err, artifactmanifest.ErrInvalid) {
			t.Errorf("%s was admitted: %v", name, err)
		}
	}
	if _, err := ParseEvent([]byte(strings.Replace(string(data), `{"kind"`, `{ "kind"`, 1))); !errors.Is(err, artifactmanifest.ErrInvalid) {
		t.Fatalf("non-canonical event was admitted: %v", err)
	}
}

func TestJournalRoundTripAndOrder(t *testing.T) {
	ctx := context.Background()
	store, scope := testScope(t)
	journal, err := Open(store, scope)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		started(time.Second, at(time.Minute)),
		{Event: Heartbeat, At: at(time.Second)},
		{Event: CancelRequested, At: at(2 * time.Second), Reason: ReasonDeadline},
		{Event: CancelAcknowledged, At: at(3 * time.Second)},
		{Event: Finished, At: at(4 * time.Second), Outcome: runnerterminal.Timeout},
	}
	for _, event := range events {
		if err := journal.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	budget := 100
	observed, err := Read(ctx, store, scope, &budget)
	if err != nil || observed.Invalid || len(observed.Events) != len(events) || budget != 100-len(events)-1 {
		t.Fatalf("journal round trip lost events: %+v budget=%d %v", observed, budget, err)
	}
	empty := 0
	if _, err := Read(ctx, store, scope, &empty); !errors.Is(err, artifactmanifest.ErrInvalid) {
		t.Fatalf("exhausted read budget did not fail closed: %v", err)
	}
	var nilJournal *Journal
	if err := nilJournal.Append(ctx, events[0]); err != nil || nilJournal.Err() != nil {
		t.Fatal("nil journal must be a no-op")
	}
}

func TestJournalOutOfOrderIsInvalid(t *testing.T) {
	for name, sequence := range map[string][]Event{
		"heartbeat first":     {{Event: Heartbeat, At: epoch}},
		"ack without request": {started(time.Second, at(time.Minute)), {Event: CancelAcknowledged, At: epoch}},
		"twice started":       {started(time.Second, at(time.Minute)), started(time.Second, at(time.Minute))},
		"event after finish": {started(time.Second, at(time.Minute)),
			{Event: Finished, At: epoch, Outcome: runnerterminal.Success}, {Event: Heartbeat, At: epoch}},
		"forced twice": {started(time.Second, at(time.Minute)), {Event: Forced, At: epoch}, {Event: Forced, At: epoch}},
		"ack and forced": {started(time.Second, at(time.Minute)), {Event: CancelRequested, At: epoch, Reason: ReasonCaller},
			{Event: CancelAcknowledged, At: epoch}, {Event: Forced, At: epoch}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, scope := testScope(t)
			journal, err := Open(store, scope)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range sequence {
				if err := journal.Append(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			observed, err := Read(ctx, store, scope, nil)
			if err != nil || !observed.Invalid || len(observed.Events) != 0 {
				t.Fatalf("out-of-order journal was trusted: %+v %v", observed, err)
			}
			if got := Derive(Facts{Status: attemptDispatched}, observed, at(time.Hour), 0); got.Observation != "invalid" || got.State != "unobserved" {
				t.Fatalf("invalid journal supported a liveness claim: %+v", got)
			}
		})
	}
}

func TestDerive(t *testing.T) {
	recorded := func(events ...Event) *Observed { return &Observed{Events: events} }
	base := started(time.Second, at(time.Minute))
	request := Event{Event: CancelRequested, At: at(2 * time.Second), Reason: ReasonCaller}
	cases := []struct {
		name       string
		facts      Facts
		observed   *Observed
		now        time.Time
		staleAfter time.Duration
		state      string
		retry      Retry
		cancel     string
		partial    bool
		deadline   bool
		actions    []string
	}{
		{"reserved", Facts{Status: "reserved"}, nil, epoch, 0, "reserved", Retry{true, "not_dispatched"}, "none", false, false, []string{"inspect", "retry_with_parent"}},
		{"dispatched without journal", Facts{Status: "dispatched"}, nil, epoch, 0, "unobserved", Retry{false, "liveness_unobserved"}, "none", false, false, []string{"inspect", "wait"}},
		{"running", Facts{Status: "dispatched"}, recorded(base, Event{Event: Heartbeat, At: at(time.Second)}), at(2 * time.Second), 0, "running", Retry{false, "in_progress"}, "none", false, false, []string{"inspect", "wait", "cancel_owner"}},
		{"cancelling", Facts{Status: "dispatched"}, recorded(base, request), at(3 * time.Second), 0, "cancelling", Retry{false, "in_progress"}, "requested", false, false, []string{"inspect", "wait"}}, //nolint:misspell // wire value
		{"stale heartbeat orphaned", Facts{Status: "dispatched"}, recorded(base), at(4 * time.Second), 0, "orphaned", Retry{true, "orphaned"}, "none", true, false, []string{"inspect", "retry_with_parent"}},
		{"explicit stale threshold", Facts{Status: "dispatched"}, recorded(base), at(4 * time.Second), time.Hour, "running", Retry{false, "in_progress"}, "none", false, false, []string{"inspect", "wait", "cancel_owner"}},
		{"past deadline orphaned", Facts{Status: "dispatched"}, recorded(base, Event{Event: Heartbeat, At: at(2 * time.Minute)}), at(2 * time.Minute), 0, "orphaned", Retry{true, "orphaned"}, "none", true, true, []string{"inspect", "retry_with_parent"}},
		{"publication incomplete", Facts{Status: "dispatched"}, recorded(base, Event{Event: Finished, At: at(time.Second), Outcome: runnerterminal.Success}), at(2 * time.Second), 0, "publication_incomplete", Retry{true, "publication_incomplete"}, "none", true, false, []string{"inspect", "retry_with_parent"}},
		{"succeeded", Facts{Status: "completed", Outcome: runnerterminal.Success, EngineCalls: 1}, nil, epoch, 0, "succeeded", Retry{false, "succeeded"}, "none", false, false, []string{"inspect", "reuse"}},
		{"evaluation failed", Facts{Status: "completed", Outcome: runnerterminal.EvaluationFailure, EngineCalls: 1}, nil, epoch, 0, "evaluation_failed", Retry{false, "graded_result"}, "none", false, false, []string{"inspect", "reuse"}},
		{"rejected", Facts{Status: "completed", Outcome: runnerterminal.InvalidInput}, nil, epoch, 0, "rejected", Retry{false, "input_rejected"}, "none", false, false, []string{"inspect", "fix_input"}},
		{"cancelled partial", Facts{Status: "completed", Outcome: runnerterminal.Canceled, EngineCalls: 1}, recorded(base, request, Event{Event: CancelAcknowledged, At: at(3 * time.Second)}), epoch, 0, "cancelled", Retry{true, "cancelled"}, "acknowledged", true, false, []string{"inspect", "retry_with_parent"}}, //nolint:misspell // wire value
		{"forced", Facts{Status: "completed", Outcome: runnerterminal.Canceled}, recorded(base, request, Event{Event: Forced, At: at(3 * time.Second)}), epoch, 0, "cancelled", Retry{true, "cancelled"}, "forced", true, false, []string{"inspect", "retry_with_parent"}},                                              //nolint:misspell // wire value
		{"timed out", Facts{Status: "completed", Outcome: runnerterminal.Timeout}, nil, epoch, 0, "timed_out", Retry{true, "timed_out"}, "acknowledged", false, true, []string{"inspect", "retry_with_parent"}},
		{"input boundary failure", Facts{Status: "completed", Outcome: runnerterminal.InfrastructureFailure, FailureBoundary: "validation"}, nil, epoch, 0, "failed", Retry{false, "input_rejected"}, "none", false, false, []string{"inspect", "fix_input"}},
		{"infrastructure failure", Facts{Status: "completed", Outcome: runnerterminal.InfrastructureFailure, FailureBoundary: "shutdown", EngineCalls: 1}, nil, epoch, 0, "failed", Retry{true, "infrastructure_failure"}, "none", true, false, []string{"inspect", "retry_with_parent"}},
		{"unverified", Facts{Status: "completion_unverified"}, nil, epoch, 0, "unverified", Retry{false, "evidence_unverified"}, "none", false, false, []string{"inspect"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.facts, tc.observed, tc.now, tc.staleAfter)
			if got.State != tc.state || got.Retry != tc.retry || got.Cancellation != tc.cancel ||
				got.Partial != tc.partial || got.DeadlineExceeded != tc.deadline || !slices.Equal(got.AllowedActions, tc.actions) {
				t.Fatalf("derived %+v", got)
			}
		})
	}
	saturated := []Event{base}
	for len(saturated) < MaxEvents-reservedFinalEvents {
		saturated = append(saturated, Event{Event: Heartbeat, At: at(time.Second)})
	}
	if got := Derive(Facts{Status: "dispatched"}, recorded(saturated...), at(30*time.Second), 0); got.State != "running" {
		t.Fatalf("heartbeat capacity exhaustion was treated as staleness: %+v", got)
	}
	if got := Derive(Facts{Status: "dispatched"}, recorded(saturated...), at(2*time.Minute), 0); got.State != "orphaned" {
		t.Fatalf("a saturated journal past its deadline was not orphaned: %+v", got)
	}
	shutdown := recorded(base, Event{Event: Forced, At: at(2 * time.Second)})
	if got := Derive(Facts{Status: "completed", Outcome: runnerterminal.Timeout}, shutdown, epoch, 0); got.Cancellation != "forced" || !got.Partial {
		t.Fatalf("forced shutdown without a request lost its evidence: %+v", got)
	}
	if observed := forcedWithoutRequest(t); observed.Invalid || len(observed.Events) != 2 {
		t.Fatalf("forced shutdown without a request was rejected: %+v", observed)
	}
	if got := Derive(Facts{Status: "dispatched"}, recorded(base), at(2*time.Second), 0); got.StaleAfterMS != 3000 || got.LastEventAt == nil || got.StartedAt == nil {
		t.Fatalf("default stale threshold or timestamps were not reported: %+v", got)
	}
}

func forcedWithoutRequest(t *testing.T) *Observed {
	t.Helper()
	ctx := context.Background()
	store, scope := testScope(t)
	journal, err := Open(store, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []Event{started(time.Second, at(time.Minute)), {Event: Forced, At: epoch}} {
		if err := journal.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := Read(ctx, store, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	return observed
}
