package executionlifecycle

import (
	"context"
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
	// DefaultWriteBound bounds each journal write so a stalled store cannot
	// extend shutdown.
	DefaultWriteBound = 2 * time.Second
)

var ErrJournalFull = errors.New("lifecycle journal is full")

// NewStore opens the lifecycle journal namespace on an initialized provider.
func NewStore(backend artifactmanifest.Backend) (*artifactmanifest.Diagnostics, error) {
	return artifactmanifest.NewDiagnostics(backend, Admit)
}

// Journal appends one attempt's events in order. A failed or unknown write
// ends the journal: the chain is never retried or forked, and execution
// outcome is never changed by journal failures.
type Journal struct {
	mu     sync.Mutex
	scope  *artifactmanifest.DiagnosticScope
	cursor *artifactmanifest.Cursor
	err    error
	bound  time.Duration
}

func Open(store *artifactmanifest.Diagnostics, scope artifactmanifest.Scope) (*Journal, error) {
	if store == nil {
		return nil, artifactmanifest.ErrUnsupportedBackend
	}
	owned, err := store.Scope(scope)
	if err != nil {
		return nil, fmt.Errorf("opening lifecycle journal: %w", err)
	}
	return &Journal{scope: owned, bound: DefaultWriteBound}, nil
}

// Append records one event within the write bound, independent of ctx
// cancellation so cancellation itself can be recorded.
func (j *Journal) Append(ctx context.Context, event Event) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return j.err
	}
	next := 0
	if j.cursor != nil {
		next = int(j.cursor.Revision) + 1
	}
	if event.Event == Heartbeat && next >= MaxEvents-reservedFinalEvents {
		return nil
	}
	if next >= MaxEvents {
		j.err = ErrJournalFull
		return j.err
	}
	payload, err := EncodeEvent(event)
	if err != nil {
		j.err = err
		return err
	}
	var pending artifactmanifest.Pending
	if j.cursor == nil {
		pending, err = j.scope.PrepareStart(payload)
	} else {
		pending, err = j.scope.PrepareAdvance(*j.cursor, payload)
	}
	if err != nil {
		j.err = err
		return err
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), j.bound)
	defer cancel()
	result, err := j.scope.Commit(writeCtx, pending)
	if err != nil {
		j.err = fmt.Errorf("recording lifecycle %s event: %w", event.Event, err)
		return j.err
	}
	cursor := result.Created.Cursor
	j.cursor = &cursor
	return nil
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
