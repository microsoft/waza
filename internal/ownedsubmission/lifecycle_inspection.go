package ownedsubmission

import (
	"context"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerunit"
)

// LifecycleInspection binds fixed captured content to one original finalized
// source. It is not a live snapshot, authenticated worker state or retry policy.
type LifecycleInspection struct {
	Kind          string                               `json:"kind"`
	SchemaVersion string                               `json:"schema_version"`
	Source        executionattempt.Source              `json:"source"`
	Verification  string                               `json:"verification"`
	Context       string                               `json:"context"`
	Snapshot      executionlifecycle.Snapshot          `json:"snapshot"`
	Runtime       execution.RuntimeObservationSnapshot `json:"runtime_observations"`
}

// InspectLifecycle uses only the caller-composed scoped Reader. Its one source
// pass shares the caller's budget; no raw provider, writer or second budget is
// constructed here. Missing finalized evidence remains unavailable.
func InspectLifecycle(ctx context.Context, reader *executionledger.Reader, source executionattempt.Source,
	asOf string, staleAfterMS int64,
) (LifecycleInspection, error) {
	if staleAfterMS <= 0 || staleAfterMS > int64((24*time.Hour)/time.Millisecond) {
		return LifecycleInspection{}, fmt.Errorf("%w: stale_after_ms must be positive and at most one day", executionlifecycle.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return LifecycleInspection{}, fmt.Errorf("reading lifecycle source: %w", err)
	}
	completed, err := reader.ReadCompleted(ctx, source, runnerunit.LifecycleCaptureArtifact, "terminal.json")
	if err != nil {
		return LifecycleInspection{}, fmt.Errorf("reading original lifecycle source: %w", err)
	}
	if err := validateSourceDisclosure(completed); err != nil {
		return LifecycleInspection{}, fmt.Errorf("admitting lifecycle source disclosure: %w", err)
	}
	capture, err := runnerunit.ValidateLifecycleCaptureSource(completed)
	if err != nil {
		return LifecycleInspection{}, fmt.Errorf("validating original lifecycle capture: %w", err)
	}
	projection, err := executionlifecycle.New(completed.Reservation.Bytes,
		*capture.OriginalAdmission.TerminalBinding(), capture.Deadline)
	if err != nil {
		return LifecycleInspection{}, err
	}
	for _, observation := range capture.Observations {
		if err := projection.Apply(observation); err != nil {
			return LifecycleInspection{}, err
		}
	}
	snapshot, err := projection.Snapshot(asOf, time.Duration(staleAfterMS)*time.Millisecond)
	if err != nil {
		return LifecycleInspection{}, err
	}
	if err := ctx.Err(); err != nil {
		return LifecycleInspection{}, fmt.Errorf("reading lifecycle source: %w", err)
	}
	return LifecycleInspection{
		Kind: "waza.execution-lifecycle-source", SchemaVersion: executionlifecycle.Version,
		Source: source, Verification: "verified-nonsnapshot-content", Context: "original-source-declarations",
		Snapshot: snapshot, Runtime: capture.Runtime,
	}, nil
}
