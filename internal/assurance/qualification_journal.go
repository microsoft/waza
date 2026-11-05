package assurance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
)

type qualificationLocalScope struct {
	Namespace                string
	OwnerBindingSHA256       string
	FilesystemContractSHA256 string
}
type qualificationLocalRegistryConfig struct {
	Root          *os.Root
	BackendPolicy qualificationDocument
	Scope         qualificationLocalScope
	IO            qualificationJournalIO
}
type qualificationReservationKey struct {
	Namespace      string `json:"namespace"`
	ContractSHA256 string `json:"contract_sha256"`
	CID            string `json:"cid"`
	ArmID          string `json:"arm_id"`
}
type qualificationLocalJournal struct {
	mu                     sync.Mutex
	config                 qualificationLocalRegistryConfig
	manifest               qualificationManifest
	claimed                bool
	freshnessTaken         bool
	closed                 bool
	poison                 error
	stopNew                bool
	evidence               qualificationPrefix
	currentnessReservation *qualificationStageReservation
}

type qualificationStageReservation struct {
	head qualificationDocument
}
type qualificationStageReservationKey struct{}

// Reserve this handle's mutation boundary using the actual read-only durable
// head, before nonce generation or external callbacks. This is not an atomic
// reservation of external authorities or protection against physical tampering.
func (journal *qualificationLocalJournal) reserveCurrentness(ctx context.Context, stage string, ordinal *uint64) (reservation *qualificationStageReservation, returnErr error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	defer func() {
		if returnErr != nil {
			journal.poison = errors.Join(journal.poison, returnErr)
			if journal.currentnessReservation == reservation {
				journal.currentnessReservation = nil
			}
		}
	}()
	if err := journal.live(); err != nil {
		return nil, err
	}
	if !journal.claimed || journal.stopNew || journal.currentnessReservation != nil {
		return nil, errors.New("qualification: live exclusive currentness stage required")
	}
	key, err := journal.key(journal.manifest)
	if err != nil {
		return nil, err
	}
	lock, err := journal.config.IO.Lock(context.WithValue(ctx, qualificationReadLockKey{}, struct{}{}), journal.config.Root)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Unlock()) }()
	name, err := qualificationReservationName(key)
	if err != nil {
		return nil, err
	}
	child, err := journal.config.IO.OpenDirectDirectory(ctx, journal.config.Root, name)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	prefix, err := journal.readChild(ctx, child, key, journal.manifest.context)
	journal.evidence = prefix
	if err != nil {
		return nil, err
	}
	if prefix.final != nil || len(prefix.pending) != 0 || prefix.nextStage != stage ||
		(stage == "before_job" && (ordinal == nil || *ordinal != prefix.nextOrdinal)) ||
		(stage != "before_job" && ordinal != nil) ||
		!slices.Contains([]string{"before_job", "after_cleanup", "before_decision"}, stage) {
		return nil, errors.New("qualification: currentness stage/ordinal differs from durable next transition")
	}
	reservation = &qualificationStageReservation{head: prefix.document}
	journal.currentnessReservation = reservation
	return reservation, nil
}

func (journal *qualificationLocalJournal) releaseCurrentness(reservation *qualificationStageReservation) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.currentnessReservation == reservation {
		journal.currentnessReservation = nil
	}
}

func qualificationOpenLocalJournal(ctx context.Context, config qualificationLocalRegistryConfig) (*qualificationLocalJournal, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, errors.New("qualification: local physical filesystem backend unsupported")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Root == nil || config.IO == nil || !qualificationIdentifier(config.Scope.Namespace) ||
		!qualificationDigest(config.Scope.OwnerBindingSHA256) || !qualificationDigest(config.Scope.FilesystemContractSHA256) {
		return nil, errors.New("qualification: explicit caller-accepted physical root, scope and I/O required")
	}
	policy, err := qualificationParseBackendPolicy(config.BackendPolicy.bytes())
	if err != nil {
		return nil, err
	}
	wire, err := qualificationDecode[qualificationBackendPolicy](policy.bytes(), qualificationDocumentLimit)
	if err != nil || wire.Namespace != config.Scope.Namespace || wire.DurabilityContractSHA256 != config.Scope.FilesystemContractSHA256 {
		return nil, errors.New("qualification: runtime caller scope/policy mismatch")
	}
	config.BackendPolicy = policy
	return &qualificationLocalJournal{config: config}, nil
}

func (journal *qualificationLocalJournal) live() error {
	if journal.closed {
		return errors.New("qualification: journal closed")
	}
	if journal.poison != nil {
		return errors.Join(errors.New("qualification: journal poisoned; read-only recovery only"), journal.poison)
	}
	return nil
}
func qualificationReservationName(key qualificationReservationKey) (string, error) {
	if !qualificationIdentifier(key.Namespace) || !qualificationDigest(key.ContractSHA256) ||
		!qualificationIdentifier(key.CID) || !qualificationIdentifier(key.ArmID) {
		return "", errors.New("qualification: full local reservation key required")
	}
	document, err := qualificationSeal(key)
	if err != nil {
		return "", err
	}
	return "reservation-" + document.sha256(), nil
}
func (journal *qualificationLocalJournal) key(manifest qualificationManifest) (qualificationReservationKey, error) {
	m, err := qualificationManifestValue(manifest)
	if err != nil {
		return qualificationReservationKey{}, err
	}
	return qualificationReservationKey{journal.config.Scope.Namespace, m.Inputs.Association.ContractSHA256, m.Inputs.Association.CID, m.Inputs.Association.ArmID}, nil
}
func qualificationSequenceName(kind string, sequence uint64) string {
	return fmt.Sprintf("%s-%05d.json", kind, sequence)
}

func (journal *qualificationLocalJournal) durable(ctx context.Context, root *os.Root, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := journal.config.IO.WriteExclusive(ctx, root, name, data); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := journal.config.IO.SyncFile(ctx, root, name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.Join(journal.config.IO.SyncDirectory(ctx, root), ctx.Err())
}

func (journal *qualificationLocalJournal) acknowledgment(manifest qualificationManifest, event qualificationDocument, sequence uint64, previous, payload *string) (qualificationDocument, error) {
	view, err := qualificationNewManifestView(manifest)
	if err != nil {
		return qualificationDocument{}, err
	}
	return journal.acknowledgmentView(manifest, event, sequence, previous, payload, view)
}
func (journal *qualificationLocalJournal) acknowledgmentView(manifest qualificationManifest, event qualificationDocument, sequence uint64, previous, payload *string, view *qualificationManifestView) (qualificationDocument, error) {
	if view.manifest.document.canonical != manifest.document.canonical {
		return qualificationDocument{}, errors.New("qualification: exact manifest/backend view mismatch")
	}
	wire := qualificationAcknowledgment{
		Kind: "waza.qualification-acknowledgment", Version: qualificationVersion, InvocationID: view.invocationID,
		ManifestSHA256: manifest.document.sha256(), BackendProfileSHA256: journal.config.BackendPolicy.sha256(),
		Sequence: sequence, EventSHA256: event.sha256(), PreviousSHA256: previous, PayloadSHA256: payload,
		ReceiptToken: event.sha256(),
	}
	return qualificationSeal(wire)
}

func (journal *qualificationLocalJournal) Claim(ctx context.Context, manifest qualificationManifest) (ack qualificationDocument, returnErr error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.currentnessReservation != nil {
		return ack, errors.New("qualification: currentness callback owns mutation boundary")
	}
	defer func() {
		if returnErr != nil {
			journal.poison = errors.Join(journal.poison, returnErr)
		}
	}()
	if err := journal.live(); err != nil {
		return ack, err
	}
	if journal.claimed {
		return ack, errors.New("qualification: every fresh Claim is one-shot")
	}
	valid, err := qualificationParseManifest(manifest.document.bytes(), manifest.context)
	if err != nil {
		return ack, err
	}
	m, err := qualificationManifestValue(valid)
	if err != nil {
		return ack, err
	}
	policy, err := qualificationSeal(m.Inputs.BackendProfile)
	if err != nil || policy.canonical != journal.config.BackendPolicy.canonical {
		return ack, errors.New("qualification: admitted backend differs from accepted runtime backend")
	}
	lock, err := journal.config.IO.Lock(ctx, journal.config.Root)
	if err != nil {
		return ack, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Unlock()) }()
	key, err := journal.key(valid)
	if err != nil {
		return ack, err
	}
	name, err := qualificationReservationName(key)
	if err != nil {
		return ack, err
	}
	child, err := journal.config.IO.CreateDirectDirectory(ctx, journal.config.Root, name)
	if err != nil {
		return ack, errors.Join(errors.New("qualification: prior local reservation excludes fresh Claim; no resume"), err)
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	journal.claimed, journal.manifest = true, valid
	journal.evidence.manifest = valid
	if err := journal.config.IO.SyncDirectory(ctx, journal.config.Root); err != nil {
		return ack, err
	}
	keyDoc, err := qualificationSeal(key)
	if err != nil {
		return ack, err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"reservation.json", keyDoc.bytes()}, {"manifest.json", valid.document.bytes()}} {
		if err := journal.durable(ctx, child, file.name, file.data); err != nil {
			return ack, err
		}
	}
	ack, err = journal.acknowledgment(valid, valid.document, 0, nil, nil)
	if err != nil {
		return ack, err
	}
	if err := journal.durable(ctx, child, qualificationSequenceName("ack", 0), ack.bytes()); err != nil {
		journal.evidence.pending = append(journal.evidence.pending, ack)
		return ack, err
	}
	journal.evidence.ack = ack
	return ack, ctx.Err()
}

func (journal *qualificationLocalJournal) Append(ctx context.Context, event qualificationEvent) (qualificationDocument, error) {
	return journal.mutate(ctx, nil, event)
}
func (journal *qualificationLocalJournal) CommitTerminal(ctx context.Context, terminal qualificationTerminal, event qualificationEvent) (qualificationDocument, error) {
	return journal.mutate(ctx, &terminal, event)
}
func (journal *qualificationLocalJournal) mutate(ctx context.Context, terminal *qualificationTerminal, event qualificationEvent) (ack qualificationDocument, returnErr error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	reservation, _ := ctx.Value(qualificationStageReservationKey{}).(*qualificationStageReservation)
	if journal.currentnessReservation != nil && reservation != journal.currentnessReservation {
		return ack, errors.New("qualification: currentness callback owns mutation boundary")
	}
	defer func() {
		if returnErr != nil {
			journal.poison = errors.Join(journal.poison, returnErr)
			journal.evidence.pending = append(journal.evidence.pending, event.document)
			if terminal != nil {
				journal.evidence.pending = append(journal.evidence.pending, terminal.document)
			}
		}
	}()
	if err := journal.live(); err != nil {
		return ack, err
	}
	if !journal.claimed {
		return ack, errors.New("qualification: uniquely fresh claimed journal required")
	}
	if journal.stopNew {
		return ack, errors.New("qualification: failed operational terminal forbids new work")
	}
	lock, err := journal.config.IO.Lock(ctx, journal.config.Root)
	if err != nil {
		return ack, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Unlock()) }()
	key, err := journal.key(journal.manifest)
	if err != nil {
		return ack, err
	}
	name, err := qualificationReservationName(key)
	if err != nil {
		return ack, err
	}
	child, err := journal.config.IO.OpenDirectDirectory(ctx, journal.config.Root, name)
	if err != nil {
		return ack, err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	prefix, replay, err := journal.readChildWithReplay(ctx, child, key, journal.manifest.context)
	journal.evidence = prefix
	if err != nil {
		return ack, err
	}
	if prefix.manifest.document.canonical != journal.manifest.document.canonical {
		return ack, errors.New("qualification: persisted manifest differs from uniquely claimed manifest")
	}
	if prefix.final != nil {
		return ack, errors.New("qualification: terminal publication forbids mutation")
	}
	if reservation != nil && (journal.currentnessReservation != reservation || prefix.document.canonical != reservation.head.canonical) {
		return ack, errors.New("qualification: reserved durable head changed before currentness append")
	}
	parsed, err := qualificationParseEventBoundedView(event.document.bytes(), prefix.manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil, replay.view)
	if err != nil {
		return ack, err
	}
	wire, err := qualificationDecode[qualificationEventWire](parsed.document.bytes(), qualificationDocumentLimit)
	if err != nil {
		return ack, err
	}
	if (terminal != nil) != (wire.Type == "job_terminal") {
		return ack, errors.New("qualification: terminal payload barrier cannot be bypassed")
	}
	if terminal != nil {
		valid, err := qualificationParseTerminalBoundedView(terminal.document.bytes(), prefix.manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil, replay.view)
		if err != nil || qualificationMatchTerminalEvent(valid, wire) != nil {
			return ack, errors.New("qualification: exact terminal payload linkage required")
		}
	}
	ack, err = journal.acknowledgmentView(prefix.manifest, parsed.document, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256, replay.view)
	if err != nil {
		return ack, err
	}
	ackValue, err := qualificationDecode[qualificationAcknowledgment](ack.bytes(), qualificationDocumentLimit)
	if err != nil {
		return ack, err
	}
	recordDoc, err := qualificationSeal(qualificationRecordWire{wire, ackValue})
	if err != nil {
		return ack, err
	}
	candidate := replay.fork()
	// Freshly verified persisted head plus one exact transition rejects stale CAS.
	if err := candidate.advance(qualificationRecord{recordDoc}); err != nil {
		return ack, err
	}
	candidatePrefix, err := candidate.prefix()
	if err != nil {
		return ack, err
	}
	if terminal != nil {
		payloadName, err := qualificationArtifactFilename("job_terminal_payload", wire.Ordinal)
		if err != nil {
			return ack, err
		}
		if err := journal.durable(ctx, child, payloadName, terminal.document.bytes()); err != nil {
			return ack, err
		}
	}
	if err := journal.durable(ctx, child, qualificationSequenceName("event", wire.Sequence), parsed.document.bytes()); err != nil {
		return ack, err
	}
	if err := journal.durable(ctx, child, qualificationSequenceName("ack", wire.Sequence), ack.bytes()); err != nil {
		return ack, err
	}
	journal.evidence = candidatePrefix
	if terminal != nil {
		payload, decodeErr := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
		if decodeErr != nil {
			return ack, decodeErr
		}
		journal.stopNew = payload.ObservationState != "observed"
	}
	return ack, errors.Join(err, ctx.Err())
}

func (journal *qualificationLocalJournal) Read(ctx context.Context, key qualificationReservationKey, admitted qualificationAdmittedInputContext) (prefix qualificationPrefix, returnErr error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return prefix, errors.New("qualification: journal closed")
	}
	if key.Namespace != journal.config.Scope.Namespace {
		return prefix, errors.New("qualification: namespace differs from accepted local scope")
	}
	lock, err := journal.config.IO.Lock(context.WithValue(ctx, qualificationReadLockKey{}, struct{}{}), journal.config.Root)
	if err != nil {
		return prefix, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Unlock()) }()
	name, err := qualificationReservationName(key)
	if err != nil {
		return prefix, err
	}
	child, err := journal.config.IO.OpenDirectDirectory(ctx, journal.config.Root, name)
	if err != nil {
		return prefix, err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	return journal.readChild(ctx, child, key, admitted)
}

func (journal *qualificationLocalJournal) readChild(ctx context.Context, child *os.Root, key qualificationReservationKey, admitted qualificationAdmittedInputContext) (prefix qualificationPrefix, returnErr error) {
	prefix, _, err := journal.readChildWithReplay(ctx, child, key, admitted)
	return prefix, err
}
func (journal *qualificationLocalJournal) readChildWithReplay(ctx context.Context, child *os.Root, key qualificationReservationKey, admitted qualificationAdmittedInputContext) (prefix qualificationPrefix, replay *qualificationReplayState, returnErr error) {
	keyData, err := journal.config.IO.ReadDirect(ctx, child, "reservation.json", qualificationDocumentLimit)
	if err != nil {
		return prefix, replay, err
	}
	expected, err := qualificationSeal(key)
	if err != nil || string(keyData) != expected.canonical {
		return prefix, replay, errors.New("qualification: full persisted reservation differs")
	}
	data, err := journal.config.IO.ReadDirect(ctx, child, "manifest.json", qualificationDocumentLimit)
	if err != nil {
		return prefix, replay, err
	}
	manifest, view, err := qualificationParseManifestView(data, admitted)
	prefix.manifest = manifest
	if err != nil || string(data) != manifest.document.canonical {
		return prefix, replay, errors.New("qualification: historical source manifest invalid")
	}
	actualKey := qualificationReservationKey{journal.config.Scope.Namespace, view.contractSHA256, view.cid, view.armID}
	if actualKey != key {
		return prefix, replay, errors.New("qualification: historical association mismatch")
	}
	ackData, err := journal.config.IO.ReadDirect(ctx, child, qualificationSequenceName("ack", 0), qualificationDocumentLimit)
	if err != nil {
		prefix.pending = append(prefix.pending, manifest.document)
		return prefix, replay, err
	}
	prefix.ack, err = qualificationParseAcknowledgment(ackData)
	if err != nil || string(ackData) != prefix.ack.canonical {
		return prefix, replay, errors.New("qualification: historical manifest acknowledgment invalid")
	}
	replay, err = qualificationBeginReplay(view, prefix.ack)
	if err != nil {
		return prefix, replay, err
	}
	directory, err := child.Open(".")
	if err != nil {
		return prefix, replay, err
	}
	names, listErr := directory.Readdirnames(3*qualificationArtifactLimit + 1)
	closeErr := directory.Close()
	if listErr != nil && !errors.Is(listErr, io.EOF) {
		return prefix, replay, errors.Join(listErr, closeErr)
	}
	if closeErr != nil || len(names) >= 3*qualificationArtifactLimit+1 {
		return prefix, replay, errors.Join(errors.New("qualification: historical file count"), closeErr)
	}
	files := map[string]bool{}
	for _, name := range names {
		files[name] = true
	}
	delete(files, "reservation.json")
	delete(files, "manifest.json")
	delete(files, qualificationSequenceName("ack", 0))
	total := uint64(len(data) + len(ackData) + len(keyData))
	for sequence := uint64(1); sequence <= qualificationArtifactLimit; sequence++ {
		eventName, ackName := qualificationSequenceName("event", sequence), qualificationSequenceName("ack", sequence)
		if !files[eventName] {
			break
		}
		eventData, err := journal.config.IO.ReadDirect(ctx, child, eventName, min(qualificationDocumentLimit, qualificationTotalLimit-total))
		if err != nil {
			return prefix, replay, err
		}
		total += uint64(len(eventData))
		event, err := qualificationParseEventBoundedView(eventData, manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil, view)
		if err != nil || string(eventData) != event.document.canonical {
			return prefix, replay, errors.New("qualification: historical event invalid")
		}
		value, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
		if err != nil {
			return prefix, replay, err
		}
		var pendingPayload qualificationDocument
		if value.Type == "job_terminal" {
			payloadName, err := qualificationArtifactFilename("job_terminal_payload", value.Ordinal)
			if err != nil {
				return prefix, replay, err
			}
			payload, err := journal.config.IO.ReadDirect(ctx, child, payloadName, min(qualificationDocumentLimit, qualificationTotalLimit-total))
			if err != nil {
				return prefix, replay, err
			}
			total += uint64(len(payload))
			terminal, err := qualificationParseTerminalBoundedView(payload, manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil, view)
			pendingPayload = qualificationDocument{canonical: string(payload)}
			if err != nil || qualificationMatchTerminalEvent(terminal, value) != nil || string(payload) != terminal.document.canonical {
				prefix.pending = append(prefix.pending, event.document, pendingPayload)
				return prefix, replay, errors.New("qualification: historical payload invalid")
			}
			delete(files, payloadName)
		}
		if !files[ackName] {
			prefix.pending = append(prefix.pending, event.document)
			if pendingPayload.canonical != "" {
				prefix.pending = append(prefix.pending, pendingPayload)
			}
			return prefix, replay, errors.New("qualification: stored event lacks durable acknowledgment")
		}
		ackData, err := journal.config.IO.ReadDirect(ctx, child, ackName, min(qualificationDocumentLimit, qualificationTotalLimit-total))
		if err != nil {
			return prefix, replay, err
		}
		total += uint64(len(ackData))
		ack, err := qualificationParseAcknowledgment(ackData)
		if err != nil || string(ackData) != ack.canonical {
			return prefix, replay, errors.New("qualification: historical acknowledgment invalid")
		}
		ackWire, err := qualificationDecode[qualificationAcknowledgment](ack.bytes(), qualificationDocumentLimit)
		if err != nil {
			return prefix, replay, err
		}
		record, err := qualificationSeal(qualificationRecordWire{value, ackWire})
		if err != nil {
			return prefix, replay, err
		}
		if err := replay.advance(qualificationRecord{record}); err != nil {
			return prefix, replay, err
		}
		derived, err := replay.prefix()
		if err != nil {
			return prefix, replay, err
		}
		prefix = derived
		delete(files, eventName)
		delete(files, ackName)
	}
	if len(files) != 0 {
		for name := range files {
			if strings.HasPrefix(name, "job_terminal_payload-") {
				payload, err := journal.config.IO.ReadDirect(ctx, child, name, min(qualificationDocumentLimit, qualificationTotalLimit-total))
				if err == nil {
					total += uint64(len(payload))
					prefix.pending = append(prefix.pending, qualificationDocument{canonical: string(payload)})
				}
			}
		}
		return prefix, replay, errors.New("qualification: orphan/conflicting suffix; read-only incomplete")
	}
	if len(replay.records) == 0 && replay.final == nil {
		empty, err := replay.prefix()
		return empty, replay, err
	}
	return prefix, replay, nil
}

func (journal *qualificationLocalJournal) Close() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.closed = true
	return nil
}

func (journal *qualificationLocalJournal) Evidence() qualificationPrefix {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	copy := journal.evidence
	copy.records, copy.pending = slices.Clone(copy.records), slices.Clone(copy.pending)
	if copy.final != nil {
		copy.final = new(*copy.final)
	}
	return copy
}
