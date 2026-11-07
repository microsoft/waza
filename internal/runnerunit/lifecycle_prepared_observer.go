package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// PreparedLifecycleObserver stages one invocation's fixed metadata. Only the
// submission publisher writes its admitted inventory; Terminal verifies only.
type PreparedLifecycleObserver struct {
	mu       sync.Mutex
	now      func() time.Time
	ledger   *executionledger.Ledger
	creator  executionledger.Creator
	original *executionledger.Reservation
	deadline string
	started  executionlifecycle.Event
	runtime  *execution.OwnedRuntimeObservations
	prepared *LifecycleCapture
	terminal []byte
	verified bool
	failure  error
}

func NewPreparedLifecycleObserver(now func() time.Time) (*PreparedLifecycleObserver, error) {
	if now == nil {
		return nil, fmt.Errorf("prepared lifecycle observation requires an explicit clock")
	}
	return &PreparedLifecycleObserver{now: now}, nil
}

func (o *PreparedLifecycleObserver) Hooks() SubmissionHooks {
	return SubmissionHooks{Reserved: o.reserved, SelectLifecycle: o.selectLifecycle, Terminal: o.verifyTerminal}
}

func (o *PreparedLifecycleObserver) reserved(ctx context.Context, ledger *executionledger.Ledger,
	creator executionledger.Creator, callback executionledger.Reservation) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.original != nil {
		return fmt.Errorf("prepared lifecycle observer already belongs to one submission")
	}
	if deadline, present := ctx.Deadline(); present {
		o.deadline = deadline.UTC().Format(time.RFC3339Nano)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	original, err := originalReservation(ctx, ledger, creator, callback)
	if err != nil {
		return fmt.Errorf("admitting prepared lifecycle observer: %w", err)
	}
	o.ledger, o.creator, o.original = ledger, creator, original
	o.runtime = execution.NewOwnedRuntimeObservations()
	o.started = o.event("started", 1, nil)
	projection, err := executionlifecycle.New(original.Bytes, *original.Attempt.Binding.TerminalBinding(), o.deadline)
	if err == nil {
		err = projection.Apply(o.started)
	}
	o.failure = err
	return err
}

func (o *PreparedLifecycleObserver) RuntimeObservations() (*execution.OwnedRuntimeObservations, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failure != nil {
		return nil, o.failure
	}
	if o.original == nil || o.runtime == nil || o.prepared != nil {
		return nil, executionledger.ErrCapability
	}
	return o.runtime, nil
}

func (o *PreparedLifecycleObserver) event(kind string, sequence int, terminal *runnerterminal.Envelope) executionlifecycle.Event {
	return executionlifecycle.Event{
		Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
		ExecutionID: o.original.Attempt.ExecutionID, ReservationDigest: o.original.Digest,
		Sequence: sequence, ObservedAt: o.now().UTC().Format(time.RFC3339Nano), Event: kind, Terminal: terminal,
	}
}

func (o *PreparedLifecycleObserver) selectLifecycle(ctx context.Context, ledger *executionledger.Ledger,
	creator executionledger.Creator, selection LifecycleSelection) (capture LifecycleCapture, failure error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failure != nil {
		return capture, o.failure
	}
	if o.original == nil || o.ledger != ledger || o.creator != creator {
		return capture, executionledger.ErrCapability
	}
	if o.prepared != nil {
		return capture, fmt.Errorf("%w: lifecycle metadata is already sealed", executionlifecycle.ErrTransition)
	}
	defer func() {
		if failure != nil {
			o.failure = failure
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	original, err := originalReservation(ctx, ledger, creator, selection.Reservation)
	if err != nil {
		return capture, err
	}
	if !bytes.Equal(original.Bytes, o.original.Bytes) || selection.Deadline != o.deadline {
		return capture, fmt.Errorf("%w: prepared lifecycle changes retained context or invocation deadline", executionlifecycle.ErrContext)
	}
	terminal, err := runnerterminal.Encode(selection.Terminal, original.Attempt.Binding.TerminalBinding())
	if err != nil {
		return capture, err
	}
	detached, err := runnerterminal.Validate(terminal, original.Attempt.Binding.TerminalBinding())
	if err != nil {
		return capture, err
	}
	native, err := o.runtime.Seal()
	if err != nil {
		return capture, fmt.Errorf("sealing prepared native lifecycle evidence: %w", err)
	}
	binding := original.Attempt.Binding
	binding.GraderIDs = slices.Clone(binding.GraderIDs)
	capture = LifecycleCapture{
		Kind: "waza.execution-lifecycle-capture", SchemaVersion: executionlifecycle.Version,
		Reservation: slices.Clone(original.Bytes), OriginalAdmission: binding, ReservationDigest: original.Digest,
		Deadline: o.deadline, Observations: []executionlifecycle.Event{o.started, o.event("terminal_recorded", 2, detached)},
		ObservationDigests: []string{}, Runtime: native,
	}
	projection, err := executionlifecycle.New(original.Bytes, *binding.TerminalBinding(), o.deadline)
	if err != nil {
		return capture, err
	}
	for _, event := range capture.Observations {
		if err := projection.Apply(event); err != nil {
			return capture, err
		}
		data, err := executionlifecycle.Encode(event)
		if err != nil {
			return capture, err
		}
		capture.ObservationDigests = append(capture.ObservationDigests, artifactmanifest.Hash(data))
	}
	retained, err := clonePreparedLifecycle(capture)
	if err != nil {
		return LifecycleCapture{}, err
	}
	o.prepared, o.terminal = &retained, slices.Clone(terminal)
	return capture, nil
}

func clonePreparedLifecycle(capture LifecycleCapture) (LifecycleCapture, error) {
	data, err := json.Marshal(capture)
	if err != nil {
		return LifecycleCapture{}, err
	}
	var detached LifecycleCapture
	if err := json.Unmarshal(data, &detached); err != nil {
		return LifecycleCapture{}, err
	}
	return detached, nil
}

func (o *PreparedLifecycleObserver) verifyTerminal(ctx context.Context, ledger *executionledger.Ledger,
	creator executionledger.Creator, callback executionledger.Reservation, terminal runnerterminal.Envelope) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failure != nil {
		return o.failure
	}
	if o.original == nil || o.prepared == nil || o.ledger != ledger || o.creator != creator {
		return executionledger.ErrCapability
	}
	if o.verified {
		return fmt.Errorf("%w: lifecycle terminal was already verified", executionlifecycle.ErrTransition)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	original, err := originalReservation(ctx, ledger, creator, callback)
	if err != nil {
		return err
	}
	data, err := runnerterminal.Encode(terminal, original.Attempt.Binding.TerminalBinding())
	if err != nil {
		return err
	}
	if !bytes.Equal(original.Bytes, o.original.Bytes) || !bytes.Equal(data, o.terminal) {
		return fmt.Errorf("%w: terminal differs from sealed original lifecycle metadata", executionlifecycle.ErrContext)
	}
	o.verified = true
	return nil
}
