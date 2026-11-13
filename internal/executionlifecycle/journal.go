package executionlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

const (
	// MaxEvents is the diagnostics revision capacity of one journal.
	MaxEvents = artifactmanifest.MaxDiagnosticRevision + 1
	// Heartbeats stop before the final slots so cancellation and finish
	// events always fit.
	reservedFinalEvents = 4
	// DefaultHeartbeatInterval is the owner's heartbeat cadence.
	DefaultHeartbeatInterval = 10 * time.Second
	// DefaultWriteBound bounds each caller's wait for a journal write so a
	// stalled store cannot extend shutdown.
	DefaultWriteBound = 2 * time.Second
)

var (
	ErrJournalFull = errors.New("lifecycle journal is full")
	// ErrWriteStalled ends a journal whose write outlived its bound. The
	// abandoned write may still complete; Wait reports when it has.
	ErrWriteStalled = errors.New("lifecycle journal write exceeded its bound")
	// ErrUnauthorized denies writes through a read-only lifecycle store.
	ErrUnauthorized = errors.New("lifecycle journal write is not authorized")
)

// NewStore opens the lifecycle journal namespace read-only on an initialized
// provider. Its admission denies every write; owners use Open.
func NewStore(backend artifactmanifest.Backend) (*artifactmanifest.Diagnostics, error) {
	return artifactmanifest.NewDiagnostics(backend, func(context.Context, artifactmanifest.Scope, json.RawMessage) error {
		return ErrUnauthorized
	})
}

// Authorize revalidates the owner's retained creator and invocation
// authority. It runs at every commit, before any provider I/O; the public
// scope IDs alone never authorize a write.
type Authorize func(context.Context) error

// Journal appends one attempt's events in order. A failed, unknown or stalled
// write ends the journal: the chain is never retried or forked, and execution
// outcome is never changed by journal failures.
type Journal struct {
	serial   sync.Mutex // orders appends; each holds it for at most one wait bound
	mu       sync.Mutex
	scope    *artifactmanifest.DiagnosticScope
	cursor   *artifactmanifest.Cursor
	err      error
	bound    time.Duration
	inflight int           // guarded by mu
	idle     chan struct{} // guarded by mu; closed when inflight drops to zero
}

// Open binds a writable journal to scope and the owner's authority.
func Open(backend artifactmanifest.Backend, scope artifactmanifest.Scope, authorize Authorize) (*Journal, error) {
	if authorize == nil {
		return nil, fmt.Errorf("%w: lifecycle journal requires owner authority", artifactmanifest.ErrInvalid)
	}
	store, err := artifactmanifest.NewDiagnostics(backend, func(ctx context.Context, actual artifactmanifest.Scope, payload json.RawMessage) error {
		if actual != scope {
			return fmt.Errorf("%w: lifecycle write targets another attempt", ErrUnauthorized)
		}
		if _, err := ParseEvent(payload); err != nil {
			return err
		}
		if err := authorize(ctx); err != nil {
			return fmt.Errorf("%w: %w", ErrUnauthorized, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	owned, err := store.Scope(scope)
	if err != nil {
		return nil, fmt.Errorf("opening lifecycle journal: %w", err)
	}
	return &Journal{scope: owned, bound: DefaultWriteBound}, nil
}

type committed struct {
	result artifactmanifest.WriteResult
	err    error
}

// Append records one event, independent of ctx cancellation so cancellation
// itself can be recorded. The caller waits at most the write bound even when
// provider I/O ignores its context.
func (j *Journal) Append(ctx context.Context, event Event) error {
	if j == nil {
		return nil
	}
	j.serial.Lock()
	defer j.serial.Unlock()
	if err := j.Err(); err != nil {
		return err
	}
	next := 0
	if j.cursor != nil {
		next = int(j.cursor.Revision) + 1
	}
	if event.Event == Heartbeat && next >= MaxEvents-reservedFinalEvents {
		return nil
	}
	if next >= MaxEvents {
		return j.fail(ErrJournalFull)
	}
	payload, err := EncodeEvent(event)
	if err != nil {
		return j.fail(err)
	}
	var pending artifactmanifest.Pending
	if j.cursor == nil {
		pending, err = j.scope.PrepareStart(payload)
	} else {
		pending, err = j.scope.PrepareAdvance(*j.cursor, payload)
	}
	if err != nil {
		return j.fail(err)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j.bound)
	done := make(chan committed, 1)
	j.mu.Lock()
	if j.inflight == 0 {
		j.idle = make(chan struct{})
	}
	j.inflight++
	j.mu.Unlock()
	go func() {
		defer cancel()
		result, err := j.scope.Commit(writeCtx, pending)
		done <- committed{result, err}
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.inflight--; j.inflight == 0 {
			close(j.idle)
		}
	}()
	// A short grace lets a context-honoring provider report its own cause.
	wait := time.NewTimer(j.bound + j.bound/2)
	defer wait.Stop()
	select {
	case commit := <-done:
		if commit.err != nil {
			return j.fail(fmt.Errorf("recording lifecycle %s event: %w", event.Event, commit.err))
		}
		cursor := commit.result.Created.Cursor
		j.cursor = &cursor
		return nil
	case <-wait.C:
		return j.fail(fmt.Errorf("recording lifecycle %s event: %w", event.Event, ErrWriteStalled))
	}
}

func (j *Journal) fail(err error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err == nil {
		j.err = err
	}
	return j.err
}

// Wait waits up to timeout for writes abandoned after their bound. The
// provider must stay open until Wait returns nil.
func (j *Journal) Wait(timeout time.Duration) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	if j.inflight == 0 {
		j.mu.Unlock()
		return nil
	}
	idle := j.idle
	j.mu.Unlock()
	wait := time.NewTimer(timeout)
	defer wait.Stop()
	select {
	case <-idle:
		return nil
	case <-wait.C:
		return fmt.Errorf("%w: an abandoned write is still in flight", ErrWriteStalled)
	}
}

// Err reports the first write failure; later events were not recorded.
func (j *Journal) Err() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// Observed is a read-only view of one attempt's journal. Invalid journals keep
// no events: a broken chain cannot support liveness or cancellation claims.
type Observed struct {
	Events  []Event
	Invalid bool
}

// Read follows one journal chain from revision zero until the first missing
// revision. Storage and context errors are returned; corrupt, unchained or
// out-of-order records mark the journal invalid without hiding other evidence.
func Read(ctx context.Context, store *artifactmanifest.Diagnostics, scope artifactmanifest.Scope, budget *int) (*Observed, error) {
	observed := &Observed{Events: []Event{}}
	if store == nil {
		return observed, nil
	}
	owned, err := store.Scope(scope)
	if err != nil {
		return nil, err
	}
	var previous *artifactmanifest.Cursor
	seen := map[EventType]bool{}
	for revision := uint32(0); revision <= artifactmanifest.MaxDiagnosticRevision; revision++ {
		if budget != nil {
			if *budget <= 0 {
				return nil, fmt.Errorf("%w: lifecycle journal read budget exhausted", artifactmanifest.ErrInvalid)
			}
			*budget--
		}
		record, err := owned.ReadRevision(ctx, revision)
		switch {
		case errors.Is(err, artifactmanifest.ErrMissing):
			return observed, nil
		case errors.Is(err, artifactmanifest.ErrCorrupt), errors.Is(err, artifactmanifest.ErrInvalid):
			return &Observed{Events: []Event{}, Invalid: true}, nil
		case err != nil:
			return nil, fmt.Errorf("reading lifecycle journal: %w", err)
		}
		if previous == nil && record.Previous != nil || previous != nil && (record.Previous == nil || *record.Previous != *previous) {
			return &Observed{Events: []Event{}, Invalid: true}, nil
		}
		event, err := ParseEvent(record.Payload)
		if err != nil || !admissible(seen, *event) {
			return &Observed{Events: []Event{}, Invalid: true}, nil
		}
		seen[event.Event] = true
		observed.Events = append(observed.Events, *event)
		cursor := record.Cursor
		previous = &cursor
	}
	return observed, nil
}

// admissible enforces the owner's event order: started first and once;
// acknowledgement only after a request; force with or without one (shutdown
// can exceed its bound without cancellation); at most one of them; nothing
// after finished.
func admissible(seen map[EventType]bool, next Event) bool {
	switch {
	case !seen[Started]:
		return next.Event == Started
	case seen[Finished], next.Event == Started:
		return false
	case next.Event == CancelRequested:
		return !seen[CancelRequested]
	case next.Event == CancelAcknowledged:
		return seen[CancelRequested] && !seen[CancelAcknowledged] && !seen[Forced]
	case next.Event == Forced:
		return !seen[CancelAcknowledged] && !seen[Forced]
	}
	return true
}
