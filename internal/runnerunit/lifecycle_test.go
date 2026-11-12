package runnerunit

import (
	"context"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
)

type hangingShutdown struct{ *instrumented }

func (e *hangingShutdown) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func lineageAttempt(t *testing.T, backend artifactmanifest.Backend, result Submission) executionledger.LineageAttempt {
	t.Helper()
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := ledger.Lineage(context.Background(), result.Reservation.Logical, executionledger.LineageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range lineage.Attempts {
		if attempt.ExecutionID == result.Reservation.ExecutionID {
			return attempt
		}
	}
	t.Fatalf("attempt %s missing from lineage", result.Reservation.ExecutionID)
	return executionledger.LineageAttempt{}
}

func TestSubmitRecordsOwnerLifecycle(t *testing.T) {
	previous := runtimeShutdownBound
	runtimeShutdownBound = 20 * time.Millisecond
	t.Cleanup(func() { runtimeShutdownBound = previous })
	cases := []struct {
		name         string
		mode         string
		timeoutMS    int
		cancel       bool
		hang         bool
		state        string
		cancellation string
		reason       string
		retry        bool
		partial      bool
		deadline     bool
	}{
		{name: "completed", state: "succeeded", cancellation: "none"},
		{name: "caller cancellation", mode: "canceled", cancel: true, state: "cancelled", cancellation: "acknowledged", reason: "caller", retry: true, partial: true}, //nolint:misspell // wire value
		{name: "deadline", mode: "timeout", timeoutMS: 30, state: "timed_out", cancellation: "acknowledged", reason: "deadline", retry: true, partial: true, deadline: true},
		// The shutdown bound surfaces as a timeout terminal; the journal keeps
		// the caller's reason and the forced stop.
		{name: "forced without request", hang: true, state: "timed_out", cancellation: "forced", reason: "deadline", retry: true, partial: true, deadline: true},
		{name: "forced", mode: "canceled", cancel: true, hang: true, state: "timed_out", cancellation: "forced", reason: "caller", retry: true, partial: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := submissionBackend(t)
			request := fixture(t)
			if tc.timeoutMS != 0 {
				request.TimeoutMS = tc.timeoutMS
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := NewService(nil)
			service.heartbeat = 2 * time.Millisecond
			service.dispatch = func(ctx context.Context, report Report, input *admitted) Report {
				// Let at least one heartbeat land before execution.
				time.Sleep(10 * time.Millisecond)
				report.Mode = "mock"
				base := &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID), mode: tc.mode}
				var engine execution.AgentEngine = base
				if tc.hang {
					engine = &hangingShutdown{base}
				}
				if tc.cancel {
					cancel()
				}
				return execute(ctx, report, input, engine)
			}
			result, err := service.Submit(ctx, submissionWire(t, request, nil), backend, SubmissionHooks{})
			if err != nil || result.Status != "completed" || result.LifecycleError() != nil {
				t.Fatalf("submission failed: %+v %v %v", result, err, result.LifecycleError())
			}
			lifecycle := lineageAttempt(t, backend, result).Lifecycle
			if lifecycle.State != tc.state || lifecycle.Observation != "recorded" || lifecycle.Cancellation != tc.cancellation ||
				lifecycle.CancellationReason != tc.reason || lifecycle.Retry.Eligible != tc.retry || lifecycle.Partial != tc.partial || lifecycle.DeadlineExceeded != tc.deadline ||
				lifecycle.DeadlineAt == nil || lifecycle.HeartbeatIntervalMS != 2 || lifecycle.Events < 3 {
				t.Fatalf("lifecycle %+v", lifecycle)
			}
		})
	}
}

func TestLifecycleJournalFailureKeepsOutcome(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := executionlifecycle.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	execID, _ := executionattempt.NewExecutionID()
	decisionID, _ := executionattempt.NewDecisionID()
	scope, err := executionlifecycle.Scope(execID, artifactmanifest.Hash([]byte("reservation")), decisionID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := executionlifecycle.Open(store, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	want := Report{Status: "completed", Calls: 1, Diagnostics: []runnercontract.Diagnostic{}}
	got := trackLifecycle(context.Background(), journal, time.Millisecond, func(context.Context) Report {
		time.Sleep(5 * time.Millisecond)
		return want
	})
	finishLifecycle(context.Background(), journal, runnerterminal.Success)
	if got.Status != want.Status || got.Calls != want.Calls || journal.Err() == nil {
		t.Fatalf("journal failure changed outcome or was hidden: %+v %v", got, journal.Err())
	}
}
