package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// LifecycleSelection contains original context and the admitted protected
// terminal, never a rich report. Deadline is the retained invocation deadline,
// not the detached publication context's deadline.
type LifecycleSelection struct {
	Reservation executionledger.Reservation
	Terminal    runnerterminal.Envelope
	Deadline    string
}

// LifecycleSelector stages fixed owned metadata without persistence. The owner
// admits and publishes its inventory once; later Terminal callbacks verify only.
// A configured producer remains responsible for truthful observation provenance.
type LifecycleSelector func(context.Context, *executionledger.Ledger, executionledger.Creator, LifecycleSelection) (LifecycleCapture, error)

func selectLifecycle(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator,
	selector LifecycleSelector, terminal runnerterminal.Envelope, deadline string) ([]SubmissionArtifact, error) {
	if err := ledger.ValidateCreator(ctx, creator); err != nil {
		return nil, err
	}
	original, err := ledger.Reservation(creator)
	if err != nil {
		return nil, err
	}
	data, err := runnerterminal.Encode(terminal, original.Attempt.Binding.TerminalBinding())
	if err != nil {
		return nil, err
	}
	detached, err := runnerterminal.Validate(data, original.Attempt.Binding.TerminalBinding())
	if err != nil {
		return nil, err
	}
	callback, err := ledger.Reservation(creator)
	if err != nil {
		return nil, err
	}
	capture, err := selector(ctx, ledger, creator, LifecycleSelection{
		Reservation: *callback, Terminal: *detached, Deadline: deadline,
	})
	if err != nil {
		return nil, err
	}
	return admitLifecycleCapture(capture, *original, data, deadline)
}

func admitLifecycleCapture(capture LifecycleCapture, original executionledger.Reservation, terminal []byte, deadline string) ([]SubmissionArtifact, error) {
	binding := original.Attempt.Binding
	bindingBytes, err := executionattempt.EncodeAttempt(executionattempt.Attempt{
		Kind: original.Attempt.Kind, SchemaVersion: original.Attempt.SchemaVersion,
		Logical: original.Attempt.Logical, ExecutionID: original.Attempt.ExecutionID,
		Binding: capture.OriginalAdmission, Parent: original.Attempt.Parent,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encoding selected lifecycle admission: %w", executionlifecycle.ErrContext, err)
	}
	if !bytes.Equal(bindingBytes, original.Bytes) ||
		!bytes.Equal(capture.Reservation, original.Bytes) || capture.ReservationDigest != original.Digest ||
		capture.Kind != "waza.execution-lifecycle-capture" || capture.SchemaVersion != executionlifecycle.Version ||
		capture.Deadline != deadline {
		return nil, fmt.Errorf("%w: selected lifecycle contradicts original context or invocation deadline", executionlifecycle.ErrContext)
	}
	if err := validateRuntimeCapture(capture.Runtime); err != nil {
		return nil, err
	}
	if len(capture.Observations) < 2 || len(capture.Observations) > executionlifecycle.MaxEvents ||
		len(capture.ObservationDigests) != len(capture.Observations) {
		return nil, fmt.Errorf("%w: selected lifecycle inventory is incomplete or exceeds its bound", executionlifecycle.ErrInvalid)
	}
	projection, err := executionlifecycle.New(original.Bytes, *binding.TerminalBinding(), deadline)
	if err != nil {
		return nil, err
	}
	artifacts := make([]SubmissionArtifact, 0, len(capture.Observations)+1)
	for i, event := range capture.Observations {
		data, err := executionlifecycle.Encode(event)
		if err != nil {
			return nil, err
		}
		if event.Sequence != i+1 || capture.ObservationDigests[i] != artifactmanifest.Hash(data) {
			return nil, fmt.Errorf("%w: selected lifecycle order or digest contradicts canonical event", executionlifecycle.ErrContext)
		}
		if err := projection.Apply(event); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, SubmissionArtifact{
			Name: fmt.Sprintf("lifecycle-observation-%03d.json", i+1), MediaType: "application/json", Data: data,
		})
	}
	last := capture.Observations[len(capture.Observations)-1]
	if last.Event != "terminal_recorded" || last.Terminal == nil {
		return nil, fmt.Errorf("%w: selected lifecycle has no original terminal", executionlifecycle.ErrTransition)
	}
	recorded, err := runnerterminal.Encode(*last.Terminal, binding.TerminalBinding())
	if err != nil {
		return nil, fmt.Errorf("%w: encoding selected lifecycle terminal: %w", executionlifecycle.ErrContext, err)
	}
	if !bytes.Equal(recorded, terminal) {
		return nil, fmt.Errorf("%w: selected lifecycle changes protected original terminal", executionlifecycle.ErrContext)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		return nil, err
	}
	if len(data) > executionlifecycle.MaxBytes {
		return nil, fmt.Errorf("%w: selected lifecycle capture exceeds byte bound", executionlifecycle.ErrInvalid)
	}
	artifacts = append(artifacts, SubmissionArtifact{Name: LifecycleCaptureArtifact, MediaType: "application/json", Data: data})
	return artifacts, nil
}

func validateRuntimeCapture(snapshot execution.RuntimeObservationSnapshot) error {
	if snapshot.ProcessExit != "unknown" || snapshot.LatePolicy != "excluded-after-seal; retained-operator-counter" ||
		snapshot.Dropped != 0 || snapshot.PendingAtSeal != 0 ||
		snapshot.Observations == nil || len(snapshot.Observations) > executionlifecycle.MaxEvents-2 ||
		(!snapshot.OwnedEngineBound && len(snapshot.Observations) != 0) {
		return fmt.Errorf("%w: selected runtime metadata is incomplete or claims unsupported process evidence", execution.ErrRuntimeObservation)
	}
	for i, observation := range snapshot.Observations {
		observed, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
		if err != nil || observed.Year() < 1 || observed.Location() != time.UTC ||
			observed.Format(time.RFC3339Nano) != observation.ObservedAt || observation.Sequence != i+1 {
			return fmt.Errorf("%w: selected runtime sequence or timestamp is invalid", execution.ErrRuntimeObservation)
		}
		valid := false
		switch observation.Operation {
		case "client-start-invoked", "client-stop-invoked", "force-stop-invoked":
			valid = observation.Scope == "sdk-client" && observation.Result == "invoked"
		case "client-start-returned", "client-stop-returned":
			valid = observation.Scope == "sdk-client" && slices.Contains([]string{"success", "error"}, observation.Result)
		case "force-stop-returned":
			valid = observation.Scope == "sdk-client" && observation.Result == "returned"
		case "force-stop-requested":
			valid = observation.Scope == "sdk-client-control" && observation.Result == "requested"
		case "cancellation-context-observed":
			valid = observation.Scope == "host-context" && slices.Contains([]string{"canceled", "deadline-exceeded"}, observation.Result)
		case "turn-abort-event-observed":
			valid = observation.Scope == "sdk-turn" && observation.Result == "observed"
		}
		if !valid {
			return fmt.Errorf("%w: selected runtime fields are not fixed native observations", execution.ErrRuntimeObservation)
		}
	}
	return nil
}
