package artifactmanifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	DiagnosticsNamespaceV1 = "execution-diagnostics-v1"
	MaxDiagnosticBytes     = 1 << 20
	MaxDiagnosticRevision  = 4095
	DiagnosticSlotOnly     = "slot_only"
)

// Scope identifies the diagnostic subject and a host-authorized invocation.
// It confines storage, not execution or observer authority.
type Scope struct {
	ExecutionID       string
	ReservationDigest string
	InvocationID      string
}

type Cursor struct {
	Scope    Scope
	Revision uint32
	Digest   string
}

// AdmitDiagnostic is a mandatory host gate for typed, capture-approved payloads
// and current invocation/state admission. Its own side effects belong to the host.
type AdmitDiagnostic func(context.Context, Scope, json.RawMessage) error

type Diagnostics struct {
	backend objectBackend
	admit   AdmitDiagnostic
}

type DiagnosticScope struct {
	store *Diagnostics
	scope Scope
}

// Pending retains an exact proposed write before I/O. Its zero value is invalid.
type Pending struct{ state *diagnosticPending }

type diagnosticPending struct {
	owner    *DiagnosticScope
	record   Record
	expected *Cursor
	key      string
}

func (p Pending) Scope() Scope {
	if p.state == nil {
		return Scope{}
	}
	return p.state.record.Cursor.Scope
}

func (p Pending) Key() string {
	if p.state == nil {
		return ""
	}
	return p.state.key
}

func (p Pending) Bytes() []byte {
	if p.state == nil {
		return nil
	}
	return bytes.Clone(p.state.record.Bytes)
}

func (p Pending) Digest() string { return p.Target().Digest }

func (p Pending) Expected() *Cursor {
	if p.state == nil {
		return nil
	}
	return cloneDiagnosticCursor(p.state.expected)
}

func (p Pending) Target() Cursor {
	if p.state == nil {
		return Cursor{}
	}
	return p.state.record.Cursor
}

// Record verifies only this slot's wire/context, not its chain or lifecycle.
type Record struct {
	Cursor       Cursor
	Bytes        []byte
	Payload      json.RawMessage
	Previous     *Cursor
	Verification string
}

type DiagnosticWriteOutcome string

const (
	DiagnosticNotAttempted DiagnosticWriteOutcome = "not_attempted"
	DiagnosticCreated      DiagnosticWriteOutcome = "created"
	DiagnosticOccupied     DiagnosticWriteOutcome = "occupied"
	DiagnosticUnknown      DiagnosticWriteOutcome = "unknown"
)

type WriteResult struct {
	Outcome      DiagnosticWriteOutcome
	Pending      Pending
	Created      *Record
	ObservedNext *Record
}

type DiagnosticObservation string

const (
	DiagnosticMatching   DiagnosticObservation = "matching"
	DiagnosticMissing    DiagnosticObservation = "missing"
	DiagnosticConflict   DiagnosticObservation = "conflict"
	DiagnosticInvalid    DiagnosticObservation = "invalid"
	DiagnosticUnreadable DiagnosticObservation = "unknown"
)

type Observation struct {
	Outcome  DiagnosticObservation
	Pending  Pending
	Observed *Record
}

// NewDiagnostics admits only initialized Local/Azure providers and a host gate.
// It does not create a runtime, observer capability, or ledger namespace.
func NewDiagnostics(backend Backend, admit AdmitDiagnostic) (*Diagnostics, error) {
	if admit == nil {
		return nil, fmt.Errorf("%w: diagnostic admission is required", ErrInvalid)
	}
	var objects objectBackend
	switch provider := backend.(type) {
	case *Local:
		if provider != nil && provider.root != nil {
			objects = provider
		}
	case *Azure:
		if provider != nil && provider.client != nil {
			objects = provider
		}
	}
	if objects == nil {
		return nil, ErrUnsupportedBackend
	}
	return &Diagnostics{backend: objects, admit: admit}, nil
}

func (d *Diagnostics) Scope(scope Scope) (*DiagnosticScope, error) {
	if d == nil || d.backend == nil || d.admit == nil {
		return nil, ErrUnsupportedBackend
	}
	if !validDiagnosticScope(scope) {
		return nil, fmt.Errorf("%w: diagnostic scope", ErrInvalid)
	}
	return &DiagnosticScope{store: d, scope: scope}, nil
}

func (s *DiagnosticScope) PrepareStart(payload json.RawMessage) (Pending, error) {
	return s.prepare(nil, payload)
}

func (s *DiagnosticScope) PrepareAdvance(expected Cursor, payload json.RawMessage) (Pending, error) {
	if s == nil || expected.Scope != s.scope || expected.Revision >= MaxDiagnosticRevision || !validDigest(expected.Digest) {
		return Pending{}, fmt.Errorf("%w: diagnostic predecessor cursor", ErrInvalid)
	}
	return s.prepare(&expected, payload)
}

func (s *DiagnosticScope) prepare(expected *Cursor, payload json.RawMessage) (Pending, error) {
	if s == nil || s.store == nil || s.store.backend == nil || s.store.admit == nil {
		return Pending{}, ErrUnsupportedBackend
	}
	revision := uint32(0)
	if expected != nil {
		revision = expected.Revision + 1
	}
	data, err := encodeDiagnostic(s.scope, revision, expected, bytes.Clone(payload))
	if err != nil {
		return Pending{}, err
	}
	record, err := parseDiagnostic(data, s.scope, revision)
	if err != nil {
		return Pending{}, fmt.Errorf("%w: prepared diagnostic: %w", ErrInvalid, err)
	}
	return Pending{state: &diagnosticPending{
		owner: s, record: record, expected: cloneDiagnosticCursor(expected),
		key: diagnosticKey(s.scope, revision),
	}}, nil
}

func (s *DiagnosticScope) validatePending(p Pending) error {
	if s == nil || s.store == nil || s.store.backend == nil || s.store.admit == nil ||
		p.state == nil || p.state.owner != s {
		return fmt.Errorf("%w: foreign or unavailable diagnostic pending write", ErrInvalid)
	}
	return nil
}

// Commit never retries a write. An unknown result retains the original Pending;
// matching reconciliation is observation only, never creator attribution.
func (s *DiagnosticScope) Commit(ctx context.Context, pending Pending) (WriteResult, error) {
	result := WriteResult{Outcome: DiagnosticNotAttempted, Pending: pending}
	if err := s.validatePending(pending); err != nil {
		return result, err
	}
	if ctx == nil {
		return result, fmt.Errorf("%w: diagnostic context is required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	payload := bytes.Clone(pending.state.record.Payload)
	if err := s.store.admit(ctx, s.scope, payload); err != nil {
		return result, fmt.Errorf("admitting diagnostic: %w", err)
	}
	if !bytes.Equal(payload, pending.state.record.Payload) {
		return result, fmt.Errorf("%w: diagnostic admission mutated its input", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if expected := pending.state.expected; expected != nil {
		previous, err := s.ReadRevision(ctx, expected.Revision)
		if err != nil {
			return result, fmt.Errorf("reading diagnostic predecessor: %w", err)
		}
		if previous.Cursor != *expected {
			return result, fmt.Errorf("%w: diagnostic predecessor differs", ErrConflict)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	created, err := s.store.backend.createObject(ctx, pending.Key(), pending.Bytes())
	if err != nil {
		result.Outcome = DiagnosticUnknown
		return result, fmt.Errorf("creating diagnostic slot: %w", err)
	}
	if !created {
		result.Outcome = DiagnosticOccupied
		record, err := s.ReadRevision(ctx, pending.Target().Revision)
		if err != nil {
			return result, errors.Join(ErrOccupied, err)
		}
		if !sameDiagnosticPrevious(record.Previous, pending.state.expected) {
			return result, errors.Join(ErrOccupied, fmt.Errorf("%w: occupied diagnostic predecessor differs", ErrConflict))
		}
		result.ObservedNext = &record
		return result, ErrOccupied
	}
	result.Outcome = DiagnosticUnknown
	record, err := s.ReadRevision(ctx, pending.Target().Revision)
	if err != nil {
		return result, fmt.Errorf("verifying diagnostic write: %w", err)
	}
	if !bytes.Equal(record.Bytes, pending.state.record.Bytes) {
		return result, fmt.Errorf("%w: created diagnostic readback differs", ErrConflict)
	}
	result.Outcome = DiagnosticCreated
	result.Created = &record
	return result, nil
}

func (s *DiagnosticScope) ReadRevision(ctx context.Context, revision uint32) (Record, error) {
	if s == nil || s.store == nil || s.store.backend == nil {
		return Record{}, ErrUnsupportedBackend
	}
	if revision > MaxDiagnosticRevision || ctx == nil {
		return Record{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	data, err := s.store.backend.readObject(ctx, diagnosticKey(s.scope, revision), MaxDiagnosticBytes)
	if err != nil {
		return Record{}, err
	}
	return parseDiagnostic(data, s.scope, revision)
}

func (s *DiagnosticScope) Reconcile(ctx context.Context, pending Pending) (Observation, error) {
	result := Observation{Outcome: DiagnosticUnreadable, Pending: pending}
	if err := s.validatePending(pending); err != nil {
		return result, err
	}
	record, err := s.ReadRevision(ctx, pending.Target().Revision)
	if err != nil {
		switch {
		case errors.Is(err, ErrCorrupt):
			result.Outcome = DiagnosticInvalid
		case errors.Is(err, ErrMissing):
			result.Outcome = DiagnosticMissing
		}
		return result, err
	}
	result.Observed = &record
	if !bytes.Equal(record.Bytes, pending.state.record.Bytes) {
		result.Outcome = DiagnosticConflict
		return result, ErrConflict
	}
	result.Outcome = DiagnosticMatching
	return result, nil
}

func cloneDiagnosticCursor(cursor *Cursor) *Cursor {
	if cursor == nil {
		return nil
	}
	copy := *cursor
	return &copy
}

func sameDiagnosticPrevious(a, b *Cursor) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
