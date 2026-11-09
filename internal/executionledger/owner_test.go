package executionledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func ownerFixture(t *testing.T) (*Ledger, *artifactmanifest.Local, string, artifactmanifest.Binding) {
	t.Helper()
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	ledger, err := New(backend)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../executionattempt/testdata/attempt.json")
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := executionattempt.ParseAttempt(data)
	if err != nil {
		t.Fatal(err)
	}
	binding := attempt.Binding
	binding.GraderIDs = []string{"text"}
	return ledger, backend, directory, binding
}

func reserveFixture(t *testing.T, l *Ledger, binding artifactmanifest.Binding) (Creator, *Reservation) {
	t.Helper()
	capability, err := l.Reserve(context.Background(), binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := l.Reservation(capability)
	if err != nil {
		t.Fatal(err)
	}
	return capability, reserved
}

// This authored terminal exercises storage/owner admission, not runtime proof.
// The external publication regression separately exercises the real runner.
func ownerPublication(t *testing.T, l *Ledger, binding artifactmanifest.Binding, finalized bool) string {
	t.Helper()
	selection, identity := binding.Selection, binding.Identity
	envelope := runnerterminal.Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: runnerterminal.Version,
		Outcome: runnerterminal.EvaluationFailure, ExitCode: 1, FailureBoundary: "none",
		Validation: "valid", EngineCalls: 1, Selection: &selection, Identity: &identity,
		Diagnostics: []runnerterminal.Diagnostic{{Code: "evaluation_failure", Message: "Authored owner fixture, not runtime evidence."}},
		Observation: &runnerterminal.Observation{
			ExecutionMode: binding.ExecutionMode, EngineCalls: 1, Selection: selection,
			RunNumber: selection.Trial, Attempts: selection.Attempt, Status: "failed",
			Validations: []runnerterminal.Verdict{},
		},
	}
	for _, grader := range binding.GraderIDs {
		envelope.Observation.Validations = append(envelope.Observation.Validations, runnerterminal.Verdict{Identifier: grader, Passed: false})
	}
	data, err := runnerterminal.Encode(envelope, binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	artifact := artifactmanifest.Artifact{Name: "terminal.json", SHA256: artifactmanifest.Hash(data), SizeBytes: int64(len(data)), MediaType: "application/json"}
	manifest := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: binding, TerminalArtifact: artifact.Name, Artifacts: []artifactmanifest.Artifact{artifact},
	}
	wire, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.store.Put(context.Background(), artifact, data); err != nil {
		t.Fatal(err)
	}
	digest, err := l.store.Create(context.Background(), wire)
	if err != nil {
		t.Fatal(err)
	}
	if finalized {
		if _, err := l.store.Finalize(context.Background(), digest); err != nil {
			t.Fatal(err)
		}
	}
	return digest
}

func TestOwnerReservationsRemainDistinctAndDetached(t *testing.T) {
	ledger, _, _, binding := ownerFixture(t)
	original := binding.GraderIDs[0]
	first, reserved := reserveFixture(t, ledger, binding)
	binding.GraderIDs[0] = "caller-mutated"
	reserved.Bytes[0] = 'X'
	reserved.Attempt.Binding.GraderIDs[0] = "receipt-mutated"
	actual, err := ledger.Reservation(first)
	if err != nil || actual.Attempt.Binding.GraderIDs[0] != original {
		t.Fatalf("creator retained mutable caller/receipt state: %+v %v", actual, err)
	}
	binding = actual.Attempt.Binding
	const workers = 12
	ids := make(chan string, workers)
	failures := make(chan error, workers)
	var joined sync.WaitGroup
	for range workers {
		joined.Go(func() {
			capability, err := ledger.Reserve(context.Background(), binding, nil)
			if err != nil {
				failures <- err
				return
			}
			receipt, err := ledger.Reservation(capability)
			if err != nil {
				failures <- err
				return
			}
			if receipt.Attempt.Binding.Selection.Attempt != binding.Selection.Attempt {
				failures <- fmt.Errorf("native ordinal was replaced")
				return
			}
			ids <- receipt.Attempt.ExecutionID
		})
	}
	joined.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{actual.Attempt.ExecutionID: true}
	for id := range ids {
		if seen[id] {
			t.Fatalf("independent reservation reused ID %s", id)
		}
		seen[id] = true
	}
	if len(seen) != workers+1 {
		t.Fatalf("only %d reservations observed", len(seen))
	}
}

type recordFault struct {
	records
	create func(context.Context, string, []byte) (bool, error)
	read   func(context.Context, string) ([]byte, error)
}

func TestReusePinnedRejectsChangedHistoricalSidecars(t *testing.T) {
	ctx := t.Context()
	ledger, _, directory, binding := ownerFixture(t)
	creator, reserved := reserveFixture(t, ledger, binding)
	digest := ownerPublication(t, ledger, binding, true)
	completed, err := ledger.Complete(ctx, creator, digest)
	if err != nil {
		t.Fatal(err)
	}
	source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
	if _, _, err := ledger.ReusePinned(ctx, binding, source, reserved.Bytes, completed.CompletionBytes); err != nil {
		t.Fatalf("unchanged historical sidecars rejected: %v", err)
	}
	_, parent := reserveFixture(t, ledger, binding)
	changed := reserved.Attempt
	changed.Parent = &executionattempt.Parent{ExecutionID: parent.Attempt.ExecutionID, Relationship: "retry"}
	reservation, err := executionattempt.EncodeAttempt(changed)
	if err != nil {
		t.Fatal(err)
	}
	updated := completed.Completion
	updated.ReservationDigest = artifactmanifest.Hash(reservation)
	completion, err := EncodeCompletion(updated)
	if err != nil {
		t.Fatal(err)
	}
	for kind, data := range map[string][]byte{"reservation": reservation, "completion": completion} {
		path := filepath.Join(directory, artifactmanifest.ExecutionLedgerNamespaceV1+"-"+kind+"-"+source.ExecutionID)
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ledger.ReadCompleted(ctx, source); err != nil {
		t.Fatalf("mutation did not retain valid historical source context: %v", err)
	}
	before, truncated, err := ledger.records.ListUnverified(ctx)
	if err != nil || truncated {
		t.Fatal("fixture cannot inspect complete decision inventory", err)
	}
	decision, reused, err := ledger.ReusePinned(ctx, binding, source, reserved.Bytes, completed.CompletionBytes)
	if !errors.Is(err, ErrContext) || decision != nil || reused != nil {
		t.Fatalf("changed historical sidecars authorized reuse: %+v %+v %v", decision, reused, err)
	}
	after, truncated, err := ledger.records.ListUnverified(ctx)
	if err != nil || truncated || len(after) != len(before) {
		t.Fatalf("rejected pinned reuse published a decision: %v %v", after, err)
	}
}

func (f recordFault) Create(ctx context.Context, key string, data []byte) (bool, error) {
	if f.create != nil {
		return f.create(ctx, key, data)
	}
	return f.records.Create(ctx, key, data)
}

func (f recordFault) ReadUnverified(ctx context.Context, key string) ([]byte, error) {
	if f.read != nil {
		return f.read(ctx, key)
	}
	return f.records.ReadUnverified(ctx, key)
}

func TestReservationErrorsNeverMintCapability(t *testing.T) {
	ioError := errors.New("injected namespace I/O")
	for _, kind := range []string{"occupied", "false-without-error", "create-error", "true-with-error", "ambiguous-commit", "read-error", "read-conflict", "entropy-error", "invalid-input", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ledger, _, _, binding := ownerFixture(t)
			original := ledger.records
			var want error
			id := "exec-" + strings.Repeat("b", 64)
			ledger.executionID = func() (string, error) { return id, nil }
			fault := recordFault{records: original}
			switch kind {
			case "occupied":
				if _, err := ledger.Reserve(context.Background(), binding, nil); err != nil {
					t.Fatal(err)
				}
				want = artifactmanifest.ErrOccupied
			case "false-without-error":
				fault.create = func(context.Context, string, []byte) (bool, error) { return false, nil }
				want = artifactmanifest.ErrOccupied
			case "create-error":
				fault.create = func(context.Context, string, []byte) (bool, error) { return false, ioError }
				want = ioError
			case "true-with-error":
				fault.create = func(context.Context, string, []byte) (bool, error) { return true, ioError }
				want = ioError
			case "ambiguous-commit":
				fault.create = func(ctx context.Context, key string, data []byte) (bool, error) {
					if _, err := original.Create(ctx, key, data); err != nil {
						return false, err
					}
					return false, ioError
				}
				want = ioError
			case "read-error":
				fault.read = func(context.Context, string) ([]byte, error) { return nil, ioError }
				want = ioError
			case "read-conflict":
				fault.read = func(context.Context, string) ([]byte, error) { return []byte("{}"), nil }
				want = ErrContext
			case "entropy-error":
				ledger.executionID = func() (string, error) { return "", ioError }
				want = ioError
			case "invalid-input":
				binding.Identity.Run = ""
				want = executionattempt.ErrInvalid
			case "canceled":
				want = context.Canceled
			}
			ledger.records = fault
			ctx := context.Background()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			capability, err := ledger.Reserve(ctx, binding, nil)
			if capability.state != nil || !errors.Is(err, want) {
				t.Fatalf("%s minted capability or lost error: %+v %v", kind, capability, err)
			}
			if err := ledger.ValidateCreator(context.Background(), capability); !errors.Is(err, ErrCapability) {
				t.Fatalf("failed reservation became owner: %v", err)
			}
			if kind == "ambiguous-commit" {
				ledger.records = original
				if _, err := ledger.ReadReservation(context.Background(), id); err != nil {
					t.Fatalf("ambiguous fixture did not actually commit: %v", err)
				}
				if got, err := ledger.Reserve(context.Background(), binding, nil); got.state != nil || !errors.Is(err, artifactmanifest.ErrOccupied) {
					t.Fatalf("read/occupied reclaimed ambiguous reservation: %+v %v", got, err)
				}
			}
		})
	}
}

func TestCreatorCompletionRequiresOriginalOwnerAndFinalizedContent(t *testing.T) {
	ctx := context.Background()
	ledger, backend, directory, binding := ownerFixture(t)
	capability, reserved := reserveFixture(t, ledger, binding)
	other, err := New(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range []Creator{{}, capability} {
		l := ledger
		if cap.state != nil {
			l = other
		}
		if got, err := l.Complete(ctx, cap, artifactmanifest.Hash(nil)); got != nil || !errors.Is(err, ErrCapability) {
			t.Fatalf("zero/foreign capability completed: %+v %v", got, err)
		}
	}
	digest := ownerPublication(t, ledger, binding, false)
	if got, err := ledger.Complete(ctx, capability, digest); got != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("unfinalized content completed: %+v %v", got, err)
	}
	if _, err := ledger.store.Finalize(ctx, digest); err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.Complete(ctx, capability, digest)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Complete(ctx, capability, digest)
	if err != nil || !bytes.Equal(again.CompletionBytes, completed.CompletionBytes) {
		t.Fatalf("same creator identical retry failed: %+v %v", again, err)
	}
	read, err := other.ReadCompleted(ctx, executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest})
	if err != nil || !bytes.Equal(read.CompletionBytes, completed.CompletionBytes) {
		t.Fatalf("actual completion read failed: %+v %v", read, err)
	}
	terminal, err := runnerterminal.Validate(read.Publication.ArtifactBytes["terminal.json"], binding.TerminalBinding())
	if err != nil || terminal.Outcome != runnerterminal.EvaluationFailure || terminal.ExitCode != 1 ||
		terminal.Observation.Attempts != binding.Selection.Attempt {
		t.Fatalf("completion repaired source failure/ordinal: %+v %v", terminal, err)
	}
	replacement := reserved.Attempt
	replacement.Binding.Selection.Attempt++
	replacement.Binding.Identity.Attempt = "attempt-" + artifactmanifest.Hash([]byte("retagged"))
	replacement.Binding.Identity.CanonicalDigest = artifactmanifest.Hash([]byte("retagged-request"))
	wire, err := executionattempt.EncodeAttempt(replacement)
	if err != nil {
		t.Fatal(err)
	}
	key := artifactmanifest.ExecutionLedgerNamespaceV1 + "-reservation-" + reserved.Attempt.ExecutionID
	if err := os.Chmod(filepath.Join(directory, key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, key), wire, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ledger.Complete(ctx, capability, digest); got != nil || !errors.Is(err, ErrContext) {
		t.Fatalf("retained creator accepted rewritten original context: %+v %v", got, err)
	}
}

func TestOwnerParentsAndVerifiedReuse(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	capability, reserved := reserveFixture(t, ledger, binding)
	child, err := ledger.Reserve(ctx, binding, &executionattempt.Parent{ExecutionID: reserved.Attempt.ExecutionID, Relationship: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	childReceipt, err := ledger.Reservation(child)
	if err != nil || childReceipt.Attempt.Parent.ExecutionID != reserved.Attempt.ExecutionID ||
		childReceipt.Attempt.ExecutionID == reserved.Attempt.ExecutionID {
		t.Fatalf("append-only parent lost: %+v %v", childReceipt, err)
	}
	if got, err := ledger.Reserve(ctx, binding, &executionattempt.Parent{ExecutionID: "exec-" + strings.Repeat("c", 64), Relationship: "branch"}); got.state != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("missing parent admitted: %+v %v", got, err)
	}
	caller := binding
	caller.Selection.Attempt++
	caller.Identity.Attempt = "attempt-" + artifactmanifest.Hash([]byte("alternative-native-ordinal"))
	caller.Identity.CanonicalDigest = artifactmanifest.Hash([]byte("alternative-wire"))
	digest := ownerPublication(t, ledger, binding, true)
	source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
	if decision, result, err := ledger.Reuse(ctx, caller, source); decision != nil || result != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("digest-only unfinished source reused: %+v %+v %v", decision, result, err)
	}
	if _, err := ledger.Complete(ctx, capability, digest); err != nil {
		t.Fatal(err)
	}
	decision, result, err := ledger.Reuse(ctx, caller, source)
	if err != nil || decision.ExecutionID != "" || decision.Source.ExecutionID != reserved.Attempt.ExecutionID ||
		result.Reservation.Attempt.Binding.Selection.Attempt != binding.Selection.Attempt ||
		decision.Binding.Selection.Attempt != caller.Selection.Attempt {
		t.Fatalf("verified reuse retagged source or conflated target ID: %+v %+v %v", decision, result, err)
	}
	stored, err := ledger.records.ReadUnverified(ctx, "decision/"+decision.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := executionattempt.EncodeDecision(*decision)
	if err != nil || !bytes.Equal(stored, encoded) {
		t.Fatalf("decision not actually persisted: %s %v", stored, err)
	}
	caller.ExecutionMode = "copilot"
	if got, result, err := ledger.Reuse(ctx, caller, source); got != nil || result != nil || err == nil {
		t.Fatalf("incompatible source context reused: %+v %+v %v", got, result, err)
	}
	if got, err := ledger.ReadCompleted(ctx, executionattempt.Source{ExecutionID: source.ExecutionID, ManifestDigest: artifactmanifest.Hash(nil)}); got != nil || !errors.Is(err, ErrContext) {
		t.Fatalf("wrong source digest admitted: %+v %v", got, err)
	}
}

func TestCompletionFaultsPreserveSameCreatorRetry(t *testing.T) {
	ctx := context.Background()
	ioError := errors.New("injected completion I/O")
	for _, kind := range []string{"create-error", "ambiguous-commit", "read-error", "read-conflict", "occupied-conflict", "false-without-error"} {
		t.Run(kind, func(t *testing.T) {
			ledger, _, _, binding := ownerFixture(t)
			capability, reserved := reserveFixture(t, ledger, binding)
			digest := ownerPublication(t, ledger, binding, true)
			original := ledger.records
			fault := recordFault{records: original}
			want := ioError
			switch kind {
			case "create-error":
				fault.create = func(context.Context, string, []byte) (bool, error) { return false, ioError }
			case "ambiguous-commit":
				fault.create = func(ctx context.Context, key string, data []byte) (bool, error) {
					if _, err := original.Create(ctx, key, data); err != nil {
						return false, err
					}
					return false, ioError
				}
			case "read-error", "read-conflict":
				fault.read = func(ctx context.Context, key string) ([]byte, error) {
					if strings.HasPrefix(key, "completion/") {
						if kind == "read-error" {
							return nil, ioError
						}
						return []byte("{}"), nil
					}
					return original.ReadUnverified(ctx, key)
				}
				if kind == "read-conflict" {
					want = ErrContext
				}
			case "occupied-conflict":
				other := Completion{Kind: "waza.execution-completion", SchemaVersion: Version,
					ExecutionID: reserved.Attempt.ExecutionID, ReservationDigest: reserved.Digest,
					ManifestDigest: artifactmanifest.Hash([]byte("different-source"))}
				data, err := EncodeCompletion(other)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := original.Create(ctx, "completion/"+other.ExecutionID, data); err != nil {
					t.Fatal(err)
				}
				want = ErrContext
			case "false-without-error":
				fault.create = func(context.Context, string, []byte) (bool, error) { return false, nil }
				want = artifactmanifest.ErrOccupied
			}
			ledger.records = fault
			if got, err := ledger.Complete(ctx, capability, digest); got != nil || !errors.Is(err, want) {
				t.Fatalf("completion %s returned success or lost cause: %+v %v", kind, got, err)
			}
			ledger.records = original
			if kind == "occupied-conflict" {
				if got, err := ledger.Complete(ctx, capability, digest); got != nil || !errors.Is(err, ErrContext) {
					t.Fatalf("conflicting completion was overwritten: %+v %v", got, err)
				}
				return
			}
			if kind == "ambiguous-commit" || kind == "read-error" || kind == "read-conflict" {
				data, err := original.ReadUnverified(ctx, "completion/"+reserved.Attempt.ExecutionID)
				if err != nil {
					t.Fatalf("fault did not actually commit completion: %v", err)
				}
				retried, err := ledger.Complete(ctx, capability, digest)
				if err != nil || !bytes.Equal(data, retried.CompletionBytes) {
					t.Fatalf("same creator retry rewrote committed evidence: %+v %v", retried, err)
				}
			} else if _, err := ledger.Complete(ctx, capability, digest); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnerAdmissionAndDecisionErrors(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	if got, err := New(nil); got != nil || !errors.Is(err, artifactmanifest.ErrUnsupportedBackend) {
		t.Fatalf("unsupported provider admitted: %+v %v", got, err)
	}
	if got, err := (*Ledger)(nil).Reserve(ctx, binding, nil); got.state != nil || !errors.Is(err, ErrCapability) {
		t.Fatalf("nil ledger minted owner: %+v %v", got, err)
	}
	if _, err := ledger.ReadReservation(ctx, "../escape"); !errors.Is(err, artifactmanifest.ErrInvalid) {
		t.Fatalf("caller-selected key admitted: %v", err)
	}
	capability, reserved := reserveFixture(t, ledger, binding)
	decision, err := ledger.Distinct(ctx, capability)
	if err != nil || decision.ExecutionID != reserved.Attempt.ExecutionID || decision.Source != nil ||
		decision.Outcome != "distinct" {
		t.Fatalf("actual distinct target declaration invalid: %+v %v", decision, err)
	}
	if got, err := ledger.Distinct(ctx, Creator{}); got != nil || !errors.Is(err, ErrCapability) {
		t.Fatalf("zero creator persisted distinct decision: %+v %v", got, err)
	}
	ledger.decisionID = func() (string, error) { return decision.DecisionID, nil }
	if got, err := ledger.Distinct(ctx, capability); got != nil || !errors.Is(err, artifactmanifest.ErrOccupied) {
		t.Fatalf("occupied identical decision hydrated success: %+v %v", got, err)
	}
	ioError := errors.New("decision entropy failed")
	ledger.decisionID = func() (string, error) { return "", ioError }
	if got, err := ledger.Distinct(ctx, capability); got != nil || !errors.Is(err, ioError) {
		t.Fatalf("decision entropy error lost: %+v %v", got, err)
	}
	other := binding
	other.Identity.Trial = "trial-" + artifactmanifest.Hash([]byte("other-logical-work"))
	if got, err := ledger.Reserve(ctx, other, &executionattempt.Parent{ExecutionID: reserved.Attempt.ExecutionID, Relationship: "branch"}); got.state != nil || !errors.Is(err, ErrContext) {
		t.Fatalf("cross-work parent admitted: %+v %v", got, err)
	}
	if _, err := ledger.Complete(ctx, capability, artifactmanifest.Hash(nil)); !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("missing manifest completed: %v", err)
	}
}

func TestIndependentOwnerCollisionGrantsOnlyOneCreator(t *testing.T) {
	ledger, backend, _, binding := ownerFixture(t)
	other, err := New(backend)
	if err != nil {
		t.Fatal(err)
	}
	id := "exec-" + strings.Repeat("f", 64)
	for _, l := range []*Ledger{ledger, other} {
		l.executionID = func() (string, error) { return id, nil }
	}
	type result struct {
		ledger *Ledger
		cap    Creator
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var joined sync.WaitGroup
	for _, l := range []*Ledger{ledger, other} {
		joined.Go(func() {
			<-start
			capability, err := l.Reserve(context.Background(), binding, nil)
			results <- result{l, capability, err}
		})
	}
	close(start)
	joined.Wait()
	close(results)
	winners, occupied := 0, 0
	for result := range results {
		if result.err == nil {
			winners++
			if err := result.ledger.ValidateCreator(context.Background(), result.cap); err != nil {
				t.Fatal(err)
			}
		} else if errors.Is(result.err, artifactmanifest.ErrOccupied) {
			occupied++
			if result.cap.state != nil {
				t.Fatal("occupied independent writer acquired creator authority")
			}
		} else {
			t.Fatal(result.err)
		}
	}

	if winners != 1 || occupied != 1 {
		t.Fatalf("actual same-key conditional results winners=%d occupied=%d", winners, occupied)
	}
}

func TestReuseRetainsCallerBindingBeforeProviderReads(t *testing.T) {
	ledger, _, _, binding := ownerFixture(t)
	capability, reserved := reserveFixture(t, ledger, binding)
	digest := ownerPublication(t, ledger, binding, true)
	if _, err := ledger.Complete(context.Background(), capability, digest); err != nil {
		t.Fatal(err)
	}
	caller := reserved.Attempt.Binding
	original := ledger.records
	ledger.records = recordFault{records: original, read: func(ctx context.Context, key string) ([]byte, error) {
		caller.GraderIDs[0] = "mutated-during-provider-read"
		return original.ReadUnverified(ctx, key)
	}}
	source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
	decision, completed, err := ledger.Reuse(context.Background(), caller, source)
	if err != nil || caller.GraderIDs[0] != "mutated-during-provider-read" ||
		decision.Binding.GraderIDs[0] != "text" || completed.Reservation.Attempt.Binding.GraderIDs[0] != "text" {
		t.Fatalf("provider read rewrote admitted caller/source binding: %+v %+v %v", decision, completed, err)
	}
}

func TestOwnedNativeGuardPrecedesNamespaceAndPreservesMock(t *testing.T) {
	for _, name := range []string{"COPILOT_CLI_PATH", "COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
		t.Run(name, func(t *testing.T) {
			ledger, _, _, binding := ownerFixture(t)
			for _, variable := range []string{"COPILOT_CLI_PATH", "COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
				t.Setenv(variable, "")
			}
			t.Setenv(name, "uncontrolled-native-override")
			original := ledger.records
			calls := 0
			ledger.records = recordFault{
				records: original,
				read: func(ctx context.Context, key string) ([]byte, error) {
					calls++
					return original.ReadUnverified(ctx, key)
				},
				create: func(ctx context.Context, key string, data []byte) (bool, error) {
					calls++
					return original.Create(ctx, key, data)
				},
			}
			binding.ExecutionMode = "copilot"
			if cap, err := ledger.Reserve(context.Background(), binding, nil); cap.state != nil ||
				!errors.Is(err, execution.ErrOwnedRuntime) || calls != 0 {
				t.Fatalf("native guard reserved/read namespace: %+v calls=%d %v", cap, calls, err)
			}
			binding.ExecutionMode = "mock"
			capability, err := ledger.Reserve(context.Background(), binding, nil)
			if err != nil || capability.state == nil || calls == 0 {
				t.Fatalf("irrelevant native override broke actual mock reservation: %+v calls=%d %v", capability, calls, err)
			}
		})
	}
}
