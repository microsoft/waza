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
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// LifecycleObserver is retained by one submission invocation. The creator
// authenticates that invocation's original context, not persisted blob provenance.
// Public inspection remains caller-declared until original publication binds it.
type LifecycleObserver struct {
	mu       sync.Mutex
	backend  artifactmanifest.Backend
	now      func() time.Time
	ledger   *executionledger.Ledger
	creator  executionledger.Creator
	original *executionledger.Reservation
	deadline string
	digests  []string
	failure  error
	runtime  *execution.OwnedRuntimeObservations
	native   *execution.RuntimeObservationSnapshot
}

func NewLifecycleObserver(backend artifactmanifest.Backend, now func() time.Time) (*LifecycleObserver, error) {
	if backend == nil || now == nil {
		return nil, fmt.Errorf("lifecycle observation requires a backend and explicit clock")
	}
	return &LifecycleObserver{backend: backend, now: now, digests: []string{}}, nil
}

func (o *LifecycleObserver) Hooks() SubmissionHooks {
	return SubmissionHooks{Reserved: o.reserved, Terminal: o.terminal}
}

func originalReservation(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator,
	callback executionledger.Reservation) (*executionledger.Reservation, error) {
	if ledger == nil {
		return nil, executionledger.ErrCapability
	}
	if err := ledger.ValidateCreator(ctx, creator); err != nil {
		return nil, err
	}
	retained, err := ledger.Reservation(creator)
	if err != nil {
		return nil, err
	}
	canonical, err := executionattempt.EncodeAttempt(callback.Attempt)
	if err != nil {
		return nil, err
	}
	if callback.Digest != retained.Digest || !bytes.Equal(callback.Bytes, retained.Bytes) ||
		!bytes.Equal(canonical, retained.Bytes) {
		return nil, fmt.Errorf("%w: lifecycle callback retags retained original admission", executionlifecycle.ErrContext)
	}
	return retained, nil
}

func (o *LifecycleObserver) reserved(ctx context.Context, ledger *executionledger.Ledger,
	creator executionledger.Creator, callback executionledger.Reservation) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.original != nil {
		return fmt.Errorf("lifecycle observer already belongs to one submission")
	}
	deadline, present := ctx.Deadline()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	original, err := originalReservation(ctx, ledger, creator, callback)
	if err != nil {
		return fmt.Errorf("admitting lifecycle observer: %w", err)
	}
	o.ledger, o.creator, o.original = ledger, creator, original
	o.runtime = execution.NewOwnedRuntimeObservations()
	if present {
		o.deadline = deadline.UTC().Format(time.RFC3339Nano)
	}
	artifact := artifactmanifest.Artifact{Name: "lifecycle-reservation", SHA256: original.Digest,
		SizeBytes: int64(len(original.Bytes)), MediaType: "application/json"}
	if err := artifactmanifest.NewStore(o.backend).Put(ctx, artifact, original.Bytes); err != nil {
		o.failure = fmt.Errorf("publishing lifecycle original at blob-%s: %w", original.Digest, err)
		return o.failure
	}
	o.failure = o.appendEvent(ctx, "started", nil)
	return o.failure
}

func (o *LifecycleObserver) terminal(ctx context.Context, ledger *executionledger.Ledger,
	creator executionledger.Creator, callback executionledger.Reservation, terminal runnerterminal.Envelope) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.original == nil || o.ledger != ledger || o.creator != creator {
		return executionledger.ErrCapability
	}
	if o.failure != nil {
		return o.failure
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	original, err := originalReservation(ctx, ledger, creator, callback)
	if err != nil {
		return fmt.Errorf("admitting lifecycle terminal observer: %w", err)
	}
	if !bytes.Equal(original.Bytes, o.original.Bytes) {
		return fmt.Errorf("%w: lifecycle terminal changed original reservation", executionlifecycle.ErrContext)
	}
	native, err := o.runtime.Seal()
	o.native = &native
	if err != nil {
		o.failure = fmt.Errorf("sealing owned runtime lifecycle evidence: %w", err)
		return o.failure
	}
	o.failure = o.appendEvent(ctx, "terminal_recorded", &terminal)
	return o.failure
}

// RuntimeObservations is selected by the fresh owned engine factory only after
// reservation. No reuse path, persisted creator or second engine can bind it.
func (o *LifecycleObserver) RuntimeObservations() (*execution.OwnedRuntimeObservations, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.original == nil || o.runtime == nil || o.native != nil {
		return nil, executionledger.ErrCapability
	}
	if o.failure != nil {
		return nil, o.failure
	}
	return o.runtime, nil
}

func (o *LifecycleObserver) appendEvent(ctx context.Context, kind string, terminal *runnerterminal.Envelope) error {
	projection, err := executionlifecycle.New(o.original.Bytes, *o.original.Attempt.Binding.TerminalBinding(), o.deadline)
	if err != nil {
		return err
	}
	blobs := executionlifecycle.NewBlobs(o.backend)
	for _, digest := range o.digests {
		previous, err := blobs.Load(ctx, digest)
		if err != nil {
			return err
		}
		if err := projection.Apply(*previous); err != nil {
			return err
		}
	}
	observed := o.now()
	if observed.IsZero() {
		return fmt.Errorf("%w: lifecycle clock has no observation", executionlifecycle.ErrInvalid)
	}
	event := executionlifecycle.Event{
		Kind: "waza.execution-lifecycle-observation", SchemaVersion: executionlifecycle.Version,
		ExecutionID: o.original.Attempt.ExecutionID, ReservationDigest: o.original.Digest,
		Sequence: len(o.digests) + 1, ObservedAt: observed.UTC().Format(time.RFC3339Nano),
		Event: kind, Terminal: terminal,
	}
	if err := projection.Apply(event); err != nil {
		return err
	}
	data, err := executionlifecycle.Encode(event)
	if err != nil {
		return err
	}
	digest, err := blobs.Put(ctx, event)
	if err != nil {
		return fmt.Errorf("publishing lifecycle observation at blob-%s: %w", artifactmanifest.Hash(data), err)
	}
	o.digests = append(o.digests, digest)
	return nil
}

// Inspection returns only explicitly retained digests after rechecking the
// creator and current blobs. Reuse/no-hook paths have no new observed invocation.
func (o *LifecycleObserver) Inspection(ctx context.Context, asOf string, staleAfterMS int64) (executionlifecycle.Inspection, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.original == nil {
		return executionlifecycle.Inspection{}, executionledger.ErrCapability
	}
	if o.failure != nil {
		return executionlifecycle.Inspection{}, o.failure
	}
	if err := o.ledger.ValidateCreator(ctx, o.creator); err != nil {
		return executionlifecycle.Inspection{}, err
	}
	binding := o.original.Attempt.Binding
	binding.GraderIDs = slices.Clone(binding.GraderIDs)
	inspection := executionlifecycle.Inspection{
		Kind: "waza.execution-lifecycle-inspection", SchemaVersion: executionlifecycle.Version,
		OriginalAdmission: binding, ReservationDigest: o.original.Digest,
		ObservationDigests: slices.Clone(o.digests), Deadline: o.deadline,
		AsOf: asOf, StaleAfterMS: staleAfterMS,
	}
	data, err := json.Marshal(inspection)
	if err != nil {
		return executionlifecycle.Inspection{}, err
	}
	if _, err := executionlifecycle.Inspect(ctx, o.backend, data); err != nil {
		return executionlifecycle.Inspection{}, err
	}
	return inspection, nil
}
