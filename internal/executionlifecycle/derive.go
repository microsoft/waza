package executionlifecycle

import (
	"time"

	"github.com/microsoft/waza/internal/runnerterminal"
)

// Ledger attempt statuses, mirrored from the execution lineage contract.
const (
	attemptReserved             = "reserved"
	attemptDispatched           = "dispatched"
	attemptCompleted            = "completed"
	attemptCompletionUnverified = "completion_unverified"
)

// Facts are the immutable ledger facts for one attempt.
type Facts struct {
	Status          string
	Outcome         runnerterminal.Outcome
	FailureBoundary string
	EngineCalls     int
}

type Retry struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

// Lifecycle is derived on read. It is machine-readable guidance for operators
// and external schedulers; Waza does not act on it or own retry policy.
type Lifecycle struct {
	State               string     `json:"state"`
	Observation         string     `json:"observation"`
	Events              int        `json:"events,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	LastEventAt         *time.Time `json:"last_event_at,omitempty"`
	DeadlineAt          *time.Time `json:"deadline_at,omitempty"`
	HeartbeatIntervalMS int64      `json:"heartbeat_interval_ms,omitempty"`
	StaleAfterMS        int64      `json:"stale_after_ms,omitempty"`
	Cancellation        string     `json:"cancellation"`
	CancellationReason  string     `json:"cancellation_reason,omitempty"`
	DeadlineExceeded    bool       `json:"deadline_exceeded"`
	Partial             bool       `json:"partial"`
	Retry               Retry      `json:"retry"`
	AllowedActions      []string   `json:"allowed_actions"`
}

// StaleFactor multiplies the recorded heartbeat interval when no explicit
// stale threshold is supplied.
const StaleFactor = 3

func timePointer(t time.Time) *time.Time { return &t }

// Derive projects lifecycle state from ledger facts and an optional journal.
// It is pure: the same inputs and clock always yield the same lifecycle.
func Derive(facts Facts, observed *Observed, now time.Time, staleAfter time.Duration) Lifecycle {
	result := Lifecycle{Observation: "unobserved", Cancellation: "none", AllowedActions: []string{}}
	var started, last, requested, finished *Event
	acknowledged, forced := false, false
	if observed != nil && observed.Invalid {
		result.Observation = "invalid"
	} else if observed != nil && len(observed.Events) != 0 {
		result.Observation, result.Events = "recorded", len(observed.Events)
		for i := range observed.Events {
			event := &observed.Events[i]
			last = event
			switch event.Event {
			case Started:
				started = event
			case CancelRequested:
				requested = event
			case CancelAcknowledged:
				acknowledged = true
			case Forced:
				forced = true
			case Finished:
				finished = event
			}
		}
	}
	threshold := staleAfter
	if started != nil {
		result.StartedAt, result.DeadlineAt = timePointer(started.At), started.DeadlineAt
		result.HeartbeatIntervalMS = started.HeartbeatIntervalMS
		if threshold <= 0 {
			threshold = time.Duration(started.HeartbeatIntervalMS) * time.Millisecond * StaleFactor
		}
		result.StaleAfterMS = max(threshold.Milliseconds(), 1)
	}
	if last != nil {
		result.LastEventAt = timePointer(last.At)
	}
	switch {
	case forced:
		result.Cancellation = "forced"
	case acknowledged:
		result.Cancellation = "acknowledged"
	case requested != nil:
		result.Cancellation = "requested"
	}
	if requested != nil {
		result.CancellationReason = requested.Reason
		result.DeadlineExceeded = requested.Reason == ReasonDeadline
	}
	// A recorded force-stop is partial-work evidence whatever the ledger
	// status; a verified graded terminal below is the only exception.
	result.Partial = forced
	set := func(state string, eligible bool, reason string, actions ...string) {
		result.State, result.Retry = state, Retry{Eligible: eligible, Reason: reason}
		result.AllowedActions = append(result.AllowedActions, actions...)
	}
	switch facts.Status {
	case attemptCompleted:
		deriveTerminal(&result, facts, finished, set)
	case attemptCompletionUnverified:
		set("unverified", false, "evidence_unverified", "inspect")
	case attemptReserved:
		set("reserved", true, "not_dispatched", "inspect", "retry_with_parent")
	case attemptDispatched:
		if result.DeadlineAt != nil && now.After(*result.DeadlineAt) {
			result.DeadlineExceeded = true
		}
		switch {
		case result.Observation != "recorded":
			set("unobserved", false, "liveness_unobserved", "inspect", "wait")
		case finished != nil:
			result.Partial = true
			set("publication_incomplete", true, "publication_incomplete", "inspect", "retry_with_parent")
		// Once heartbeats fill their capacity the owner can no longer prove
		// liveness, so silence alone is not staleness; the deadline still is.
		case now.Sub(last.At) > threshold && len(observed.Events) < MaxEvents-reservedFinalEvents ||
			result.DeadlineAt != nil && now.Sub(*result.DeadlineAt) > threshold:
			result.Partial = true
			set("orphaned", true, "orphaned", "inspect", "retry_with_parent")
		case requested != nil:
			set("cancelling", false, "in_progress", "inspect", "wait") //nolint:misspell // wire value
		default:
			set("running", false, "in_progress", "inspect", "wait", "cancel_owner")
		}
	default:
		set("unverified", false, "evidence_unverified", "inspect")
	}
	return result
}

func deriveTerminal(result *Lifecycle, facts Facts, finished *Event, set func(string, bool, string, ...string)) {
	// A published terminal means the owner joined its lifecycle, so a
	// recorded request whose acknowledgement write was lost, or a journal-less
	// cancellation, is acknowledged unless force was recorded.
	if result.Cancellation == "requested" || (facts.Outcome == runnerterminal.Canceled ||
		facts.Outcome == runnerterminal.Timeout) && result.Cancellation == "none" {
		result.Cancellation = "acknowledged"
	}
	// Without a recorded request, a timeout terminal is deadline evidence
	// unless the recorded finish precedes the deadline: then a shutdown bound,
	// not the execution deadline, ended the attempt. A recorded caller request
	// keeps its reason even when a forced shutdown surfaces as a timeout.
	if facts.Outcome == runnerterminal.Timeout && result.CancellationReason == "" &&
		(finished == nil || result.DeadlineAt == nil || !finished.At.Before(*result.DeadlineAt)) {
		result.DeadlineExceeded, result.CancellationReason = true, ReasonDeadline
	}
	graded := facts.Outcome == runnerterminal.Success || facts.Outcome == runnerterminal.EvaluationFailure
	result.Partial = !graded && (facts.EngineCalls > 0 || result.Cancellation == "forced")
	switch facts.Outcome {
	case runnerterminal.Success:
		set("succeeded", false, "succeeded", "inspect", "reuse")
	case runnerterminal.EvaluationFailure:
		set("evaluation_failed", false, "graded_result", "inspect", "reuse")
	case runnerterminal.InvalidInput, runnerterminal.UnsupportedCapability:
		set("rejected", false, "input_rejected", "inspect", "fix_input")
	case runnerterminal.Canceled:
		set("cancelled", true, "cancelled", "inspect", "retry_with_parent") //nolint:misspell // wire value
	case runnerterminal.Timeout:
		set("timed_out", true, "timed_out", "inspect", "retry_with_parent")
	default:
		switch facts.FailureBoundary {
		case "input", "validation", "negotiation", "identity":
			set("failed", false, "input_rejected", "inspect", "fix_input")
		default:
			set("failed", true, "infrastructure_failure", "inspect", "retry_with_parent")
		}
	}
}
