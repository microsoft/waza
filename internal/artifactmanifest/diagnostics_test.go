package artifactmanifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

func diagnosticFixture(t *testing.T, provider string, admit AdmitDiagnostic) (*DiagnosticScope, Backend, *fakeAzure) {
	t.Helper()
	backend, _, transport := namespaceFixture(t, provider)
	store, err := NewDiagnostics(backend, admit)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := store.Scope(Scope{
		ExecutionID: "exec-" + strings.Repeat("a", 64), ReservationDigest: strings.Repeat("b", 64),
		InvocationID: "inv-" + strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return scope, backend, transport
}

func allowDiagnostic(context.Context, Scope, json.RawMessage) error { return nil }

func prepareDiagnostic(t *testing.T, scope *DiagnosticScope, previous *Cursor, payload string) Pending {
	t.Helper()
	var pending Pending
	var err error
	if previous == nil {
		pending, err = scope.PrepareStart(json.RawMessage(payload))
	} else {
		pending, err = scope.PrepareAdvance(*previous, json.RawMessage(payload))
	}
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

func commitDiagnostic(t *testing.T, scope *DiagnosticScope, pending Pending) Record {
	t.Helper()
	result, err := scope.Commit(t.Context(), pending)
	if err != nil || result.Outcome != DiagnosticCreated || result.Created == nil ||
		result.Created.Cursor != pending.Target() || !bytes.Equal(result.Created.Bytes, pending.Bytes()) {
		t.Fatalf("known creation not verified: %+v %v", result, err)
	}
	return *result.Created
}

func TestDiagnosticsProviderContract(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			var admissions atomic.Int32
			scope, backend, _ := diagnosticFixture(t, provider, func(context.Context, Scope, json.RawMessage) error {
				admissions.Add(1)
				return nil
			})
			payload := json.RawMessage(`{"number":9007199254740993123456789,"decimal":1.2300e+40,"text":"<>&\ufffd"}`)
			start, err := scope.PrepareStart(payload)
			if err != nil {
				t.Fatal(err)
			}
			original := start.Bytes()
			payload[2] = 'X'
			start.Bytes()[0] = 'X'
			if admissions.Load() != 0 || !bytes.Equal(start.Bytes(), original) {
				t.Fatal("prepare invoked admission or retained mutable input")
			}
			first := commitDiagnostic(t, scope, start)
			if first.Verification != DiagnosticSlotOnly || !bytes.Equal(first.Payload,
				[]byte(`{"number":9007199254740993123456789,"decimal":1.2300e+40,"text":"<>&\ufffd"}`)) {
				t.Fatalf("payload precision/bytes were changed: %s", first.Payload)
			}
			first.Bytes[0], first.Payload[0] = 'X', 'X'
			next := prepareDiagnostic(t, scope, &first.Cursor, `{"state":"requested"}`)
			cursor := next.Expected()
			cursor.Digest = strings.Repeat("0", 64)
			second := commitDiagnostic(t, scope, next)
			if second.Previous == nil || *second.Previous != first.Cursor || admissions.Load() != 2 {
				t.Fatal("cursor input mutated or Commit admission was cached")
			}
			beforeReads := admissions.Load()
			for _, pending := range []Pending{start, next} {
				observation, err := scope.Reconcile(t.Context(), pending)
				if err != nil || observation.Outcome != DiagnosticMatching || observation.Observed == nil ||
					!bytes.Equal(observation.Observed.Bytes, pending.Bytes()) {
					t.Fatalf("exact reconciliation failed: %+v %v", observation, err)
				}
				read, err := scope.ReadRevision(t.Context(), pending.Target().Revision)
				if err != nil || read.Verification != DiagnosticSlotOnly {
					t.Fatalf("slot read failed: %+v %v", read, err)
				}
			}
			if admissions.Load() != beforeReads {
				t.Fatal("read-only access invoked writer admission")
			}
			result, err := scope.Commit(t.Context(), next)
			if !errors.Is(err, ErrOccupied) || result.Outcome != DiagnosticOccupied ||
				result.Created != nil || result.ObservedNext == nil {
				t.Fatalf("identical occupied record granted creation: %+v %v", result, err)
			}
			if created, err := backend.Create(t.Context(), start.Key(), start.Bytes()); created || !errors.Is(err, ErrInvalid) {
				t.Fatal("diagnostics bypassed strict artifact key profile")
			}
			ledger, err := NewNamespace(backend, ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			keys, truncated, err := ledger.ListUnverified(t.Context())
			if err != nil || truncated || len(keys) != 0 {
				t.Fatal("diagnostics leaked into execution ledger discovery")
			}
		})
	}
}

type diagnosticProbe struct {
	objectBackend
	reads, writes atomic.Int32
	create        func(context.Context, string, []byte) (bool, error)
	read          func(context.Context, string, int64) ([]byte, error)
}

func (p *diagnosticProbe) createObject(ctx context.Context, key string, data []byte) (bool, error) {
	p.writes.Add(1)
	if p.create != nil {
		return p.create(ctx, key, data)
	}
	return p.objectBackend.createObject(ctx, key, data)
}

func (p *diagnosticProbe) readObject(ctx context.Context, key string, limit int64) ([]byte, error) {
	p.reads.Add(1)
	if p.read != nil {
		return p.read(ctx, key, limit)
	}
	return p.objectBackend.readObject(ctx, key, limit)
}

func TestDiagnosticsAdmissionAndPendingBeforeIO(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			scope, backend, _ := diagnosticFixture(t, provider, allowDiagnostic)
			if store, err := NewDiagnostics(backend, nil); store != nil || !errors.Is(err, ErrInvalid) {
				t.Fatal("nil admission opened a writer")
			}
			if store, err := NewDiagnostics(nil, allowDiagnostic); store != nil || !errors.Is(err, ErrUnsupportedBackend) {
				t.Fatal("unsupported provider admitted")
			}
			invalidScope := scope.scope
			invalidScope.InvocationID = "inv-" + strings.Repeat("A", 64)
			if _, err := scope.store.Scope(invalidScope); !errors.Is(err, ErrInvalid) {
				t.Fatal("noncanonical scope admitted")
			}
			probe := &diagnosticProbe{objectBackend: scope.store.backend}
			scope.store.backend = probe
			if result, err := scope.Commit(t.Context(), Pending{}); result.Outcome != DiagnosticNotAttempted ||
				!errors.Is(err, ErrInvalid) {
				t.Fatal("zero pending admitted")
			}
			pending := prepareDiagnostic(t, scope, nil, `{"approved":false}`)
			before := pending.Bytes()
			denied := errors.New("capture projection denied")
			scope.store.admit = func(context.Context, Scope, json.RawMessage) error { return denied }
			result, err := scope.Commit(t.Context(), pending)
			if result.Outcome != DiagnosticNotAttempted || !errors.Is(err, denied) ||
				!bytes.Equal(result.Pending.Bytes(), before) {
				t.Fatalf("denial lost Pending/cause: %+v %v", result, err)
			}
			scope.store.admit = func(_ context.Context, _ Scope, payload json.RawMessage) error {
				payload[0] = 'X'
				return nil
			}
			result, err = scope.Commit(t.Context(), pending)
			if result.Outcome != DiagnosticNotAttempted || !errors.Is(err, ErrInvalid) || !bytes.Equal(pending.Bytes(), before) {
				t.Fatal("mutating admission changed frozen bytes")
			}
			other, err := scope.store.Scope(scope.scope)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := other.Commit(t.Context(), pending); result.Outcome != DiagnosticNotAttempted || !errors.Is(err, ErrInvalid) {
				t.Fatal("same scope recreated Pending instance authority")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if result, err := scope.Commit(ctx, pending); result.Outcome != DiagnosticNotAttempted || !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost before-write classification")
			}
			if probe.reads.Load() != 0 || probe.writes.Load() != 0 {
				t.Fatal("denied/mutated/canceled/foreign admission performed backend I/O")
			}
			scope.store.admit = allowDiagnostic
			commitDiagnostic(t, scope, pending)
			scope.store.admit = func(context.Context, Scope, json.RawMessage) error { return denied }
			if result, err := scope.Commit(t.Context(), pending); result.Outcome != DiagnosticNotAttempted || !errors.Is(err, denied) ||
				probe.writes.Load() != 1 {
				t.Fatal("Commit reused cached prepare/previous admission")
			}
		})
	}
}

func TestDiagnosticsConcurrentSuccessorAndStaleWriters(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		for _, identical := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/identical=%v", provider, identical), func(t *testing.T) {
				scope, _, _ := diagnosticFixture(t, provider, allowDiagnostic)
				firstPending := prepareDiagnostic(t, scope, nil, `{}`)
				first := commitDiagnostic(t, scope, firstPending)
				start := make(chan struct{})
				type outcome struct {
					result WriteResult
					err    error
				}
				results := make(chan outcome, 8)
				var ready sync.WaitGroup
				ready.Add(cap(results))
				for i := range cap(results) {
					payload := `{"state":"same"}`
					if !identical {
						payload = fmt.Sprintf(`{"contender":%d}`, i)
					}
					pending := prepareDiagnostic(t, scope, &first.Cursor, payload)
					go func() {
						ready.Done()
						<-start
						result, err := scope.Commit(t.Context(), pending)
						results <- outcome{result, err}
					}()
				}
				ready.Wait()
				close(start)
				winners := 0
				var winner Record
				for range cap(results) {
					outcome := <-results
					if outcome.result.Outcome == DiagnosticCreated && outcome.err == nil {
						winners++
						winner = *outcome.result.Created
					} else if outcome.result.Outcome != DiagnosticOccupied || !errors.Is(outcome.err, ErrOccupied) ||
						outcome.result.Created != nil || outcome.result.ObservedNext == nil {
						t.Fatalf("unexpected race result: %+v %v", outcome.result, outcome.err)
					}
				}
				if winners != 1 {
					t.Fatalf("expected one creator, got %d", winners)
				}
				stale := prepareDiagnostic(t, scope, &first.Cursor, `{"stale":true}`)
				result, err := scope.Commit(t.Context(), stale)
				if result.Outcome != DiagnosticOccupied || !errors.Is(err, ErrOccupied) ||
					!bytes.Equal(result.ObservedNext.Bytes, winner.Bytes) {
					t.Fatal("stale writer replaced winner")
				}
				original, err := scope.ReadRevision(t.Context(), 0)
				if err != nil || !bytes.Equal(original.Bytes, firstPending.Bytes()) {
					t.Fatal("concurrent writers changed prior evidence")
				}
			})
		}
	}
}

func TestDiagnosticsUnknownCommitAndReadOnlyReconciliation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		for _, failure := range []string{"lost-reply", "cancel-after-commit", "readback"} {
			t.Run(provider+"/"+failure, func(t *testing.T) {
				scope, _, _ := diagnosticFixture(t, provider, allowDiagnostic)
				original := scope.store.backend
				probe := &diagnosticProbe{objectBackend: original}
				scope.store.backend = probe
				pending := prepareDiagnostic(t, scope, nil, `{"state":"pending"}`)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				lost := errors.New("committed reply lost")
				probe.create = func(ctx context.Context, key string, data []byte) (bool, error) {
					created, err := original.createObject(ctx, key, data)
					if err != nil || !created {
						return created, err
					}
					switch failure {
					case "cancel-after-commit":
						cancel()
						return false, context.Canceled
					case "lost-reply":
						return false, lost
					default:
						probe.read = func(context.Context, string, int64) ([]byte, error) { return nil, lost }
						return true, nil
					}
				}
				result, err := scope.Commit(ctx, pending)
				if result.Outcome != DiagnosticUnknown || err == nil || result.Created != nil ||
					!bytes.Equal(result.Pending.Bytes(), pending.Bytes()) || result.Pending.Key() == "" {
					t.Fatalf("ambiguous commit minted evidence or lost Pending: %+v %v", result, err)
				}
				if failure == "cancel-after-commit" && !errors.Is(err, context.Canceled) ||
					failure != "cancel-after-commit" && !errors.Is(err, lost) {
					t.Fatal("original error cause lost")
				}
				probe.read = nil
				observation, err := scope.Reconcile(t.Context(), result.Pending)
				if err != nil || observation.Outcome != DiagnosticMatching || observation.Observed == nil ||
					probe.writes.Load() != 1 || result.Outcome != DiagnosticUnknown {
					t.Fatalf("reconciliation retried or upgraded creator attribution: %+v %v", observation, err)
				}
			})
		}
	}
}

func TestDiagnosticsAzureLostResponseAndOccupiedReadFailure(t *testing.T) {
	scope, _, transport := diagnosticFixture(t, "azure-offline-transport", allowDiagnostic)
	pending := prepareDiagnostic(t, scope, nil, `{"event":"requested"}`)
	lost := errors.New("Azure committed but transport response lost")
	transport.failCommittedPut = lost
	result, err := scope.Commit(t.Context(), pending)
	if !errors.Is(err, lost) || result.Outcome != DiagnosticUnknown || result.Created != nil {
		t.Fatalf("actual SDK transport failure granted creation: %+v %v", result, err)
	}
	observation, err := scope.Reconcile(t.Context(), result.Pending)
	if err != nil || observation.Outcome != DiagnosticMatching ||
		!bytes.Equal(observation.Observed.Bytes, pending.Bytes()) {
		t.Fatalf("actual transport did not retain committed original bytes: %+v %v", observation, err)
	}
	for _, occupied := range []struct {
		status int
		code   string
	}{{http.StatusPreconditionFailed, "ConditionNotMet"}, {http.StatusConflict, "BlobAlreadyExists"}} {
		transport.occupiedStatus, transport.occupiedCode = occupied.status, occupied.code
		result, err = scope.Commit(t.Context(), pending)
		if !errors.Is(err, ErrOccupied) || result.Outcome != DiagnosticOccupied ||
			result.Created != nil || result.ObservedNext == nil {
			t.Fatalf("SDK occupied response %s granted creation: %+v %v", occupied.code, result, err)
		}
	}
	transport.failGet = map[string]error{pending.Key(): lost}
	result, err = scope.Commit(t.Context(), pending)
	if !errors.Is(err, ErrOccupied) || !errors.Is(err, lost) || result.Outcome != DiagnosticOccupied ||
		result.Created != nil || result.ObservedNext != nil {
		t.Fatalf("occupied read failure lost status/cause: %+v %v", result, err)
	}
}

type diagnosticRetryTransport struct {
	*fakeAzure
	puts, gets  atomic.Int32
	failGetOnce atomic.Bool
	getError    error
}

func (d *diagnosticRetryTransport) Do(request *http.Request) (*http.Response, error) {
	switch request.Method {
	case http.MethodPut:
		d.puts.Add(1)
	case http.MethodGet:
		d.gets.Add(1)
		if d.failGetOnce.CompareAndSwap(true, false) {
			return nil, d.getError
		}
	}
	return d.fakeAzure.Do(request)
}

func TestDiagnosticsAzureDefaultRetriesPreserveUnknown(t *testing.T) {
	lost := errors.New("default-retry Azure committed response lost")
	transport := &diagnosticRetryTransport{
		fakeAzure: &fakeAzure{objects: make(map[string][]byte), failCommittedPut: lost},
		getError:  errors.New("transient read response lost"),
	}
	backend, err := NewAzure("https://offline.invalid/container", offlineCredential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDiagnostics(backend, allowDiagnostic)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := store.Scope(Scope{
		ExecutionID: "exec-" + strings.Repeat("a", 64), ReservationDigest: strings.Repeat("b", 64),
		InvocationID: "inv-" + strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := prepareDiagnostic(t, scope, nil, `{"event":"requested"}`)
	result, err := scope.Commit(t.Context(), pending)
	if !errors.Is(err, lost) || result.Outcome != DiagnosticUnknown || transport.puts.Load() != 1 ||
		result.Created != nil || result.ObservedNext != nil || !bytes.Equal(result.Pending.Bytes(), pending.Bytes()) {
		t.Fatalf("diagnostic create was replayed/lost original failure: outcome=%s PUTs=%d err=%v",
			result.Outcome, transport.puts.Load(), err)
	}
	transport.failGetOnce.Store(true)
	observation, err := scope.Reconcile(t.Context(), result.Pending)
	if err != nil || observation.Outcome != DiagnosticMatching || observation.Observed == nil ||
		transport.gets.Load() != 2 || transport.puts.Load() != 1 ||
		!bytes.Equal(observation.Observed.Bytes, pending.Bytes()) {
		t.Fatalf("create override changed shared read retries or retried write: observation=%s GETs=%d PUTs=%d err=%v",
			observation.Outcome, transport.gets.Load(), transport.puts.Load(), err)
	}
	result, err = scope.Commit(t.Context(), pending)
	if !errors.Is(err, ErrOccupied) || result.Outcome != DiagnosticOccupied ||
		result.ObservedNext == nil || result.Created != nil || transport.puts.Load() != 2 {
		t.Fatalf("known occupied result changed: outcome=%s PUTs=%d err=%v", result.Outcome, transport.puts.Load(), err)
	}
}

func TestDiagnosticsRejectOversizedBeforeCloning(t *testing.T) {
	var admissions atomic.Int32
	scope, _, _ := diagnosticFixture(t, "local", func(context.Context, Scope, json.RawMessage) error {
		admissions.Add(1)
		return nil
	})
	probe := &diagnosticProbe{objectBackend: scope.store.backend}
	scope.store.backend = probe
	start := prepareDiagnostic(t, scope, nil, `{}`)
	payload := bytes.Repeat([]byte("a"), 16<<20)
	copy(payload, `{"data":"`)
	copy(payload[len(payload)-2:], `"}`)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	first, firstErr := scope.PrepareStart(payload)
	next, nextErr := scope.PrepareAdvance(start.Target(), payload)
	runtime.ReadMemStats(&after)
	if !errors.Is(firstErr, ErrInvalid) || !errors.Is(nextErr, ErrInvalid) ||
		first.state != nil || next.state != nil || admissions.Load() != 0 ||
		probe.reads.Load() != 0 || probe.writes.Load() != 0 {
		t.Fatalf("oversized input was not rejected by pure prepare: %v %v", firstErr, nextErr)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 1<<20 {
		t.Fatalf("rejected 16 MiB payload allocated %d bytes before returning", allocated)
	}
}

func TestDiagnosticsCreatedReadbackValidationAndCancellation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		for _, mode := range []string{"corrupt-payload", "different-valid", "predecessor-cancel", "admission-cancel"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				scope, _, _ := diagnosticFixture(t, provider, allowDiagnostic)
				first := commitDiagnostic(t, scope, prepareDiagnostic(t, scope, nil, `{}`))
				pending := prepareDiagnostic(t, scope, &first.Cursor, `{"event":1}`)
				original := scope.store.backend
				probe := &diagnosticProbe{objectBackend: original}
				scope.store.backend = probe
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "admission-cancel" {
					scope.store.admit = func(context.Context, Scope, json.RawMessage) error {
						cancel()
						return nil
					}
				}
				probe.read = func(ctx context.Context, key string, limit int64) ([]byte, error) {
					data, err := original.readObject(ctx, key, limit)
					if key == diagnosticKey(scope.scope, 0) {
						if mode == "predecessor-cancel" {
							cancel()
						}
						return data, err
					}
					switch mode {
					case "corrupt-payload":
						return bytes.Replace(data, []byte(`{"event":1}`), []byte(`{"a":1,"a":2}`), 1), err
					case "different-valid":
						return bytes.Replace(data, []byte(`{"event":1}`), []byte(`{"event":2}`), 1), err
					default:
						return data, err
					}
				}
				result, err := scope.Commit(ctx, pending)
				if mode == "predecessor-cancel" || mode == "admission-cancel" {
					if !errors.Is(err, context.Canceled) || result.Outcome != DiagnosticNotAttempted ||
						probe.writes.Load() != 0 || mode == "admission-cancel" && probe.reads.Load() != 0 {
						t.Fatalf("precreate cancellation performed I/O/write: %+v %v", result, err)
					}
				} else {
					want := ErrConflict
					if mode == "corrupt-payload" {
						want = ErrCorrupt
					}
					if !errors.Is(err, want) || result.Outcome != DiagnosticUnknown || result.Created != nil {
						t.Fatalf("unverified created readback became success: %+v %v", result, err)
					}
					probe.read = nil
					observation, err := scope.Reconcile(t.Context(), result.Pending)
					if err != nil || observation.Outcome != DiagnosticMatching || probe.writes.Load() != 1 {
						t.Fatalf("read-only recovery rewrote/lost original bytes: %+v %v", observation, err)
					}
				}
			})
		}
	}
}

func TestDiagnosticsLocalPublicationFailures(t *testing.T) {
	for _, mode := range []string{"write", "close", "link"} {
		t.Run(mode, func(t *testing.T) {
			scope, backend, _ := diagnosticFixture(t, "local", allowDiagnostic)
			local, ok := backend.(*Local)
			if !ok {
				t.Fatal("fixture did not use local provider")
			}
			fault := errors.New("diagnostic staging " + mode)
			var staging string
			local.openStage = func(name string) (stageFile, error) {
				staging = name
				file, err := local.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					return nil, err
				}
				writer := failingWriter{File: file}
				switch mode {
				case "write":
					writer.writeErr = fault
				case "close":
					writer.closeErr = fault
				}
				return writer, nil
			}
			if mode == "link" {
				local.beforeLink = func(string, string) error { return fault }
			}
			pending := prepareDiagnostic(t, scope, nil, `{}`)
			result, err := scope.Commit(t.Context(), pending)
			if !errors.Is(err, fault) || result.Outcome != DiagnosticUnknown || result.Created != nil ||
				!bytes.Equal(result.Pending.Bytes(), pending.Bytes()) {
				t.Fatalf("publication failure lost pending/cause: %+v %v", result, err)
			}
			if _, err := local.root.Lstat(pending.Key()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed publication exposed final slot: %v", err)
			}
			if _, err := local.root.Lstat(staging); err != nil {
				t.Fatalf("staging residue was silently removed: %v", err)
			}
			observation, err := scope.Reconcile(t.Context(), pending)
			if !errors.Is(err, ErrMissing) || observation.Outcome != DiagnosticMissing {
				t.Fatalf("missing readback became no-effect proof: %+v %v", observation, err)
			}
		})
	}
}

func writeDiagnosticSlot(t *testing.T, backend Backend, transport *fakeAzure, key string, data []byte) {
	t.Helper()
	if local, ok := backend.(*Local); ok {
		if err := local.root.WriteFile(key, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		transport.mu.Lock()
		transport.objects[key] = bytes.Clone(data)
		transport.mu.Unlock()
	}
}

func TestDiagnosticsPredecessorAndOccupiedContext(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			scope, backend, transport := diagnosticFixture(t, provider, allowDiagnostic)
			probe := &diagnosticProbe{objectBackend: scope.store.backend}
			scope.store.backend = probe
			cursor := Cursor{Scope: scope.scope, Revision: 0, Digest: strings.Repeat("d", 64)}
			pending := prepareDiagnostic(t, scope, &cursor, `{}`)
			result, err := scope.Commit(t.Context(), pending)
			if result.Outcome != DiagnosticNotAttempted || !errors.Is(err, ErrMissing) || probe.writes.Load() != 0 {
				t.Fatal("missing predecessor caused write/current claim")
			}
			start := prepareDiagnostic(t, scope, nil, `{}`)
			first := commitDiagnostic(t, scope, start)
			result, err = scope.Commit(t.Context(), pending)
			if result.Outcome != DiagnosticNotAttempted || !errors.Is(err, ErrConflict) || probe.writes.Load() != 1 {
				t.Fatal("caller digest bypassed exact predecessor verification")
			}
			pending = prepareDiagnostic(t, scope, &first.Cursor, `{"good":true}`)
			for _, corrupt := range []string{"wrong-previous", "foreign-scope", "bad-payload"} {
				data := pending.Bytes()
				switch corrupt {
				case "wrong-previous":
					data = bytes.Replace(data, []byte(first.Cursor.Digest), []byte(strings.Repeat("0", 64)), 1)
				case "foreign-scope":
					data = bytes.Replace(data, []byte(scope.scope.InvocationID), []byte("inv-"+strings.Repeat("e", 64)), 1)
				case "bad-payload":
					data = bytes.Replace(data, []byte(`{"good":true}`), []byte(`{"x":1,"\u0078":2}`), 1)
				}
				writeDiagnosticSlot(t, backend, transport, pending.Key(), data)
				result, err = scope.Commit(t.Context(), pending)
				if result.Outcome != DiagnosticOccupied || !errors.Is(err, ErrOccupied) ||
					(!errors.Is(err, ErrConflict) && !errors.Is(err, ErrCorrupt)) || result.ObservedNext != nil {
					t.Fatalf("%s exposed foreign/malformed occupied record: %+v %v", corrupt, result, err)
				}
				observation, err := scope.Reconcile(t.Context(), pending)
				if err == nil || observation.Outcome == DiagnosticMatching {
					t.Fatalf("%s reconciled conflicting content: %+v %v", corrupt, observation, err)
				}
			}
		})
	}
}

func TestDiagnosticsOriginalWireAndReadParity(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			scope, backend, transport := diagnosticFixture(t, provider, allowDiagnostic)
			pending := prepareDiagnostic(t, scope, nil, `{"ok":true}`)
			for name, payload := range map[string]string{
				"duplicate": `{"x":1,"\u0078":2}`, "nested-duplicate": `{"x":[{"y":0,"y":1}]}`,
				"utf8": "{\"x\":\"\xff\"}", "surrogate": `{"x":"\ud800"}`, "low-surrogate": `{"x":"\udc00"}`,
				"null": `null`, "array": `[]`, "padded": ` {}`,
			} {
				if _, err := scope.PrepareStart(json.RawMessage(payload)); !errors.Is(err, ErrInvalid) {
					t.Fatalf("%s admitted original invalid payload: %v", name, err)
				}
			}
			for _, payload := range []string{`{"x":"\ufffd"}`, "{\"x\":\"\uFFFD\"}", `{"x":"\ud83d\ude00"}`,
				`{"x":9007199254740993123456789}`, `{"x":1e9999}`} {
				positive := prepareDiagnostic(t, scope, nil, payload)
				if !bytes.Contains(positive.Bytes(), []byte(payload)) {
					t.Fatal("valid payload tokens changed")
				}
			}
			for name, data := range map[string][]byte{
				"duplicate-payload": bytes.Replace(pending.Bytes(), []byte(`{"ok":true}`), []byte(`{"a":1,"a":2}`), 1),
				"bad-utf8":          bytes.Replace(pending.Bytes(), []byte(`{"ok":true}`), []byte("{\"x\":\"\xff\"}"), 1),
				"surrogate":         bytes.Replace(pending.Bytes(), []byte(`{"ok":true}`), []byte(`{"x":"\ud800"}`), 1),
				"exponent":          bytes.Replace(pending.Bytes(), []byte(`"revision":0`), []byte(`"revision":0e0`), 1),
				"fraction":          bytes.Replace(pending.Bytes(), []byte(`"revision":0`), []byte(`"revision":0.0`), 1),
				"negative":          bytes.Replace(pending.Bytes(), []byte(`"revision":0`), []byte(`"revision":-0`), 1),
				"missing-previous":  bytes.Replace(pending.Bytes(), []byte(`"previous":null,`), nil, 1),
				"unknown":           bytes.Replace(pending.Bytes(), []byte(`"kind":`), []byte(`"extra":null,"kind":`), 1),
			} {
				writeDiagnosticSlot(t, backend, transport, pending.Key(), data)
				if _, err := scope.ReadRevision(t.Context(), 0); !errors.Is(err, ErrCorrupt) {
					t.Fatalf("%s read repaired original wire: %v", name, err)
				}
				observation, err := scope.Reconcile(t.Context(), pending)
				if !errors.Is(err, ErrCorrupt) || observation.Outcome != DiagnosticInvalid || observation.Observed != nil {
					t.Fatalf("%s reconciliation skipped payload validator: %+v %v", name, observation, err)
				}
				result, err := scope.Commit(t.Context(), pending)
				if result.Outcome != DiagnosticOccupied || !errors.Is(err, ErrOccupied) ||
					!errors.Is(err, ErrCorrupt) || result.ObservedNext != nil {
					t.Fatalf("%s occupied read skipped validator: %+v %v", name, result, err)
				}
			}
		})
	}
}

func TestDiagnosticsBoundsAndPerSlotOnlyClaim(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			scope, backend, transport := diagnosticFixture(t, provider, allowDiagnostic)
			empty := prepareDiagnostic(t, scope, nil, `{"x":""}`)
			payload := `{"x":"` + strings.Repeat("a", MaxDiagnosticBytes-len(empty.Bytes())) + `"}`
			boundary := prepareDiagnostic(t, scope, nil, payload)
			if len(boundary.Bytes()) != MaxDiagnosticBytes {
				t.Fatalf("fixture did not reach exact envelope limit: %d", len(boundary.Bytes()))
			}
			commitDiagnostic(t, scope, boundary)
			if _, err := scope.PrepareStart(json.RawMessage(payload[:len(payload)-2] + `a"}`)); !errors.Is(err, ErrInvalid) {
				t.Fatal("one excess byte accepted")
			}
			cursor := Cursor{Scope: scope.scope, Revision: MaxDiagnosticRevision - 1, Digest: strings.Repeat("e", 64)}
			last := prepareDiagnostic(t, scope, &cursor, `{}`)
			writeDiagnosticSlot(t, backend, transport, last.Key(), last.Bytes())
			record, err := scope.ReadRevision(t.Context(), MaxDiagnosticRevision)
			if err != nil || record.Verification != DiagnosticSlotOnly || record.Previous == nil {
				t.Fatalf("individual slot read fabricated chain requirement: %+v %v", record, err)
			}
			if _, err := scope.ReadRevision(t.Context(), cursor.Revision); !errors.Is(err, ErrMissing) {
				t.Fatal("fixture predecessor unexpectedly exists")
			}
			if _, err := scope.PrepareAdvance(record.Cursor, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
				t.Fatal("revision fence escaped")
			}
			observation, err := scope.Reconcile(t.Context(), prepareDiagnostic(t, scope, &Cursor{
				Scope: scope.scope, Revision: 1, Digest: strings.Repeat("e", 64),
			}, `{}`))
			if !errors.Is(err, ErrMissing) || observation.Outcome != DiagnosticMissing {
				t.Fatal("missing reconciliation became no-effect/success")
			}
			foreign := scope.scope
			foreign.ReservationDigest = strings.Repeat("f", 64)
			if _, err := scope.PrepareAdvance(Cursor{Scope: foreign}, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
				t.Fatal("foreign reservation cursor accepted")
			}
		})
	}
}
