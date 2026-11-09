package executionledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
)

var ErrCapability = errors.New("execution creator capability is unavailable or foreign")

type records interface {
	Create(context.Context, string, []byte) (bool, error)
	ReadUnverified(context.Context, string) ([]byte, error)
	ListUnverified(context.Context) ([]string, bool, error)
}

// Ledger uses the foundation's append-only namespace and verified artifact
// reads. It is not a lease or a transaction across reservation and completion.
type Ledger struct {
	records     records
	store       *artifactmanifest.Store
	executionID func() (string, error)
	decisionID  func() (string, error)
}

// Creator is an invocation-retained capability. Its zero value is invalid;
// reading an execution ID, occupied record, or checkpoint cannot recreate it.
type Creator struct{ state *creator }

type creator struct {
	ledger *Ledger
	wire   []byte
	mu     sync.Mutex
}

type Reservation struct {
	Attempt executionattempt.Attempt
	Bytes   []byte
	Digest  string
}

type CompletionRecord struct {
	Completion Completion
	Bytes      []byte
	Digest     string
}

// CompletedExecution retains original source bytes and billing evidence. Its
// verification is declared-context/content-only, never execution attestation.
type CompletedExecution struct {
	Reservation     Reservation
	Completion      Completion
	CompletionBytes []byte
	Publication     artifactmanifest.CompletedPublication
}

func New(backend artifactmanifest.Backend) (*Ledger, error) {
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		return nil, fmt.Errorf("opening execution ledger: %w", err)
	}
	return &Ledger{records: namespace, store: artifactmanifest.NewStore(backend),
		executionID: executionattempt.NewExecutionID, decisionID: executionattempt.NewDecisionID}, nil
}

func reservation(data []byte, id string) (*Reservation, error) {
	attempt, err := executionattempt.ParseAttempt(data)
	if err != nil {
		return nil, fmt.Errorf("reading execution reservation: %w", err)
	}
	canonical, err := executionattempt.EncodeAttempt(*attempt)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) || attempt.ExecutionID != id {
		return nil, fmt.Errorf("%w: reservation is not canonical or key-bound", ErrContext)
	}
	return &Reservation{Attempt: *attempt, Bytes: bytes.Clone(data), Digest: artifactmanifest.Hash(data)}, nil
}

// ReadReservation admits canonical stored declarations, not creator authority.
func (l *Ledger) ReadReservation(ctx context.Context, id string) (*Reservation, error) {
	if l == nil || l.records == nil {
		return nil, ErrCapability
	}
	return readReservation(ctx, l.records, id)
}

func readReservation(ctx context.Context, records recordReader, id string) (*Reservation, error) {
	data, err := records.ReadUnverified(ctx, "reservation/"+id)
	if err != nil {
		return nil, fmt.Errorf("reading execution reservation: %w", err)
	}
	return reservation(data, id)
}

func (l *Ledger) Reservation(capability Creator) (*Reservation, error) {
	if l == nil || capability.state == nil || capability.state.ledger != l {
		return nil, ErrCapability
	}
	attempt, err := executionattempt.ParseAttempt(capability.state.wire)
	if err != nil {
		return nil, fmt.Errorf("reading retained creator reservation: %w", err)
	}
	return reservation(capability.state.wire, attempt.ExecutionID)
}

func (l *Ledger) ValidateCreator(ctx context.Context, capability Creator) error {
	original, err := l.Reservation(capability)
	if err != nil {
		return err
	}
	actual, err := l.ReadReservation(ctx, original.Attempt.ExecutionID)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual.Bytes, original.Bytes) {
		return fmt.Errorf("%w: original reservation was replaced", ErrContext)
	}
	return nil
}

func (l *Ledger) parent(ctx context.Context, attempt executionattempt.Attempt) error {
	lineage := []executionattempt.Attempt{attempt}
	next := attempt.Parent
	seen := map[string]bool{attempt.ExecutionID: true}
	for next != nil {
		if len(lineage) >= executionattempt.MaxLineage || seen[next.ExecutionID] {
			return fmt.Errorf("%w: parent lineage is cyclic or exceeds its bound", ErrContext)
		}
		seen[next.ExecutionID] = true
		parent, err := l.ReadReservation(ctx, next.ExecutionID)
		if err != nil {
			return fmt.Errorf("admitting execution parent: %w", err)
		}
		if parent.Attempt.Logical != attempt.Logical {
			return fmt.Errorf("%w: parent belongs to different logical work", ErrContext)
		}
		lineage = append(lineage, parent.Attempt)
		next = parent.Attempt.Parent
	}
	return executionattempt.ValidateLineage(lineage)
}

func (l *Ledger) create(ctx context.Context, key string, data []byte) error {
	created, err := l.records.Create(ctx, key, data)
	if err != nil {
		return fmt.Errorf("creating execution ledger record: %w", err)
	}
	if !created {
		return artifactmanifest.ErrOccupied
	}
	actual, err := l.records.ReadUnverified(ctx, key)
	if err != nil {
		return fmt.Errorf("reading back execution ledger record: %w", err)
	}
	if !bytes.Equal(actual, data) {
		return fmt.Errorf("%w: execution ledger readback differs", ErrContext)
	}
	return nil
}

// Reserve generates its own execution ID independently of the declared native
// ordinal. Any ambiguous/occupied/error result returns no creator capability.
func (l *Ledger) Reserve(ctx context.Context, binding artifactmanifest.Binding, parent *executionattempt.Parent) (Creator, error) {
	if l == nil || l.records == nil || l.executionID == nil {
		return Creator{}, ErrCapability
	}
	id, err := l.executionID()
	if err != nil {
		return Creator{}, err
	}
	attempt := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical: executionattempt.LogicalFrom(binding.Identity), ExecutionID: id, Binding: binding, Parent: parent,
	}
	data, err := executionattempt.EncodeAttempt(attempt)
	if err != nil {
		return Creator{}, err
	}
	// Validate the detached encoded input, not caller-owned slices or pointers.
	frozen, err := reservation(data, id)
	if err != nil {
		return Creator{}, err
	}
	if err := execution.ValidateOwnedRuntime(frozen.Attempt.Binding.ExecutionMode); err != nil {
		return Creator{}, err
	}
	if err := l.parent(ctx, frozen.Attempt); err != nil {
		return Creator{}, err
	}
	if err := l.create(ctx, "reservation/"+id, data); err != nil {
		return Creator{}, err
	}
	return Creator{state: &creator{ledger: l, wire: bytes.Clone(data)}}, nil
}

// ReadCompletion admits only the bounded canonical/key-bound declaration.
// Missing is distinct from corrupt/I/O. It grants neither ownership nor verified
// publication; use ReadCompleted with its actual manifest reference to verify.
func (l *Ledger) ReadCompletion(ctx context.Context, id string) (*CompletionRecord, error) {
	if l == nil || l.records == nil {
		return nil, ErrCapability
	}
	return readCompletion(ctx, l.records, id)
}

func readCompletion(ctx context.Context, records recordReader, id string) (*CompletionRecord, error) {
	data, err := records.ReadUnverified(ctx, "completion/"+id)
	if err != nil {
		return nil, fmt.Errorf("reading execution completion: %w", err)
	}
	completion, err := ParseCompletion(data)
	if err != nil {
		return nil, err
	}
	canonical, err := EncodeCompletion(*completion)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, data) || completion.ExecutionID != id {
		return nil, fmt.Errorf("%w: completion is not canonical or key-bound", ErrContext)
	}
	return &CompletionRecord{Completion: *completion, Bytes: bytes.Clone(data), Digest: artifactmanifest.Hash(data)}, nil
}

func (l *Ledger) ReadCompleted(ctx context.Context, source executionattempt.Source, names ...string) (*CompletedExecution, error) {
	if l == nil {
		return nil, ErrCapability
	}
	return readCompleted(ctx, l, l.store, source, names...)
}

func readCompleted(ctx context.Context, reader executionReader, store *artifactmanifest.Store, source executionattempt.Source, names ...string) (*CompletedExecution, error) {
	reserved, err := reader.ReadReservation(ctx, source.ExecutionID)
	if err != nil {
		return nil, err
	}
	record, err := reader.ReadCompletion(ctx, source.ExecutionID)
	if err != nil {
		return nil, err
	}
	completion := record.Completion
	if completion.ManifestDigest != source.ManifestDigest {
		return nil, fmt.Errorf("%w: completion contradicts original source", ErrContext)
	}
	publication, err := store.ReadCompleted(ctx, source.ManifestDigest, names...)
	if err != nil {
		return nil, fmt.Errorf("verifying completed execution artifacts: %w", err)
	}
	if err := ValidateContext(completion, reserved.Bytes, publication.ManifestBytes); err != nil {
		return nil, err
	}
	return &CompletedExecution{Reservation: *reserved, Completion: completion,
		CompletionBytes: record.Bytes, Publication: *publication}, nil
}

// Complete admits only the retained original creator and a finalized, fully
// verified manifest. Identical completion retry is authorized by that same
// creator, not by existence of the completion or reservation.
func (l *Ledger) Complete(ctx context.Context, capability Creator, digest string, names ...string) (*CompletedExecution, error) {
	reserved, err := l.Reservation(capability)
	if err != nil {
		return nil, err
	}
	capability.state.mu.Lock()
	defer capability.state.mu.Unlock()
	if err := l.ValidateCreator(ctx, capability); err != nil {
		return nil, err
	}
	publication, err := l.store.ReadCompleted(ctx, digest, names...)
	if err != nil {
		return nil, fmt.Errorf("verifying completion publication: %w", err)
	}
	completion := Completion{Kind: "waza.execution-completion", SchemaVersion: Version,
		ExecutionID: reserved.Attempt.ExecutionID, ReservationDigest: reserved.Digest, ManifestDigest: digest}
	if err := ValidateContext(completion, reserved.Bytes, publication.ManifestBytes); err != nil {
		return nil, err
	}
	data, err := EncodeCompletion(completion)
	if err != nil {
		return nil, err
	}
	key := "completion/" + completion.ExecutionID
	created, err := l.records.Create(ctx, key, data)
	if err != nil && (created || !errors.Is(err, artifactmanifest.ErrOccupied)) {
		return nil, fmt.Errorf("creating execution completion: %w", err)
	}
	if !created && err == nil {
		return nil, fmt.Errorf("creating execution completion: %w", artifactmanifest.ErrOccupied)
	}
	actual, err := l.records.ReadUnverified(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("verifying execution completion readback: %w", err)
	}
	if !bytes.Equal(actual, data) {
		return nil, fmt.Errorf("%w: occupied completion names different bytes", ErrContext)
	}
	return &CompletedExecution{Reservation: *reserved, Completion: completion,
		CompletionBytes: bytes.Clone(actual), Publication: *publication}, nil
}

func (l *Ledger) decision(ctx context.Context, binding artifactmanifest.Binding, policy, id string, source *executionattempt.Source, completed *CompletedExecution) (*executionattempt.Decision, error) {
	decisionID, err := l.decisionID()
	if err != nil {
		return nil, err
	}
	decision := executionattempt.Decision{
		Kind: "waza.duplicate-decision", SchemaVersion: executionattempt.Version, DecisionID: decisionID,
		Logical: executionattempt.LogicalFrom(binding.Identity), Binding: binding, Policy: policy,
		Outcome: "distinct", ExecutionID: id, Source: source,
	}
	if policy == "reuse" {
		decision.Outcome = "reused"
		if err := ValidateReuseContext(decision, completed.Reservation.Bytes, completed.CompletionBytes, completed.Publication.ManifestBytes); err != nil {
			return nil, err
		}
	}
	data, err := executionattempt.EncodeDecision(decision)
	if err != nil {
		return nil, err
	}
	if err := l.create(ctx, "decision/"+decisionID, data); err != nil {
		return nil, err
	}
	return executionattempt.ParseDecision(data)
}

func (l *Ledger) Distinct(ctx context.Context, capability Creator) (*executionattempt.Decision, error) {
	if err := l.ValidateCreator(ctx, capability); err != nil {
		return nil, err
	}
	reserved, err := l.Reservation(capability)
	if err != nil {
		return nil, err
	}
	return l.decision(ctx, reserved.Attempt.Binding, "distinct", reserved.Attempt.ExecutionID, nil, nil)
}

func (l *Ledger) Reuse(ctx context.Context, binding artifactmanifest.Binding, source executionattempt.Source, names ...string) (*executionattempt.Decision, *CompletedExecution, error) {
	return l.reuse(ctx, binding, source, nil, nil, names...)
}

// ReusePinned requires the final source read used by the decision to retain
// independently admitted sidecars, rather than only the manifest reference.
func (l *Ledger) ReusePinned(ctx context.Context, binding artifactmanifest.Binding, source executionattempt.Source,
	reservation, completion []byte, names ...string,
) (*executionattempt.Decision, *CompletedExecution, error) {
	if len(reservation) == 0 || len(completion) == 0 {
		return nil, nil, fmt.Errorf("%w: pinned reuse requires both original sidecars", ErrContext)
	}
	return l.reuse(ctx, binding, source, bytes.Clone(reservation), bytes.Clone(completion), names...)
}

func (l *Ledger) reuse(ctx context.Context, binding artifactmanifest.Binding, source executionattempt.Source,
	reservation, completion []byte, names ...string,
) (*executionattempt.Decision, *CompletedExecution, error) {
	binding.GraderIDs = slices.Clone(binding.GraderIDs)
	completed, err := l.ReadCompleted(ctx, source, names...)
	if err != nil {
		return nil, nil, err
	}
	if reservation != nil && (!bytes.Equal(reservation, completed.Reservation.Bytes) ||
		!bytes.Equal(completion, completed.CompletionBytes)) {
		return nil, nil, fmt.Errorf("%w: reuse source sidecars changed after admission", ErrContext)
	}
	decision, err := l.decision(ctx, binding, "reuse", "", &source, completed)
	if err != nil {
		return nil, nil, err
	}
	return decision, completed, nil
}
