package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
)

func TestBeforeAbsenceRejectsAggregateReadFailures(t *testing.T) {
	for _, control := range []struct {
		err    error
		absent bool
	}{
		{artifactmanifest.ErrMissing, true},
		{fmt.Errorf("read completion: %w", artifactmanifest.ErrMissing), true},
		{errors.Join(artifactmanifest.ErrMissing, errors.New("close failed")), false},
		{errors.New("read denied"), false},
		{nil, false},
	} {
		if actual := completionAbsent(control.err); actual != control.absent {
			t.Fatalf("completion absence for %v = %v, want %v", control.err, actual, control.absent)
		}
	}
}

func beforeCheckpointSource(t *testing.T) (Checkpoint, *artifactmanifest.Local, *executionledger.Ledger,
	executionledger.Creator, []byte, []byte,
) {
	t.Helper()
	checkpoint, binding, _, _, manifest, terminal := checkpointContext(t, false)
	backend, _ := localCheckpointStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(context.Background(), binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := PublishBefore(context.Background(), backend, ledger, creator,
		readFixture(t, "../../examples/runner/unit.json"), checkpoint.Compatibility.RunnerRevision)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Load(context.Background(), backend, digest)
	if err != nil {
		t.Fatal(err)
	}
	// The native producer supplies an independent valid source graph. These
	// helper tests do not establish the public pre-factory lifecycle boundary.
	return *before, backend, ledger, creator, manifest, terminal
}

func TestBeforeCheckpointReadsOriginalReservationWithoutCreatorHydration(t *testing.T) {
	checkpoint, _, ledger, creator, _, _ := beforeCheckpointSource(t)
	request := readFixture(t, "../../examples/runner/unit.json")
	changed := bytes.Replace(request, []byte(`"attempt": 2`), []byte(`"attempt": 3`), 1)
	if bytes.Equal(changed, request) {
		t.Fatal("destination ordinal control did not change the actual request")
	}
	current, err := ForRequest(changed, checkpoint.Compatibility.RunnerRevision)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := ReadBefore(context.Background(), ledger, checkpoint, current)
	if err != nil {
		t.Fatal(err)
	}
	original, err := ledger.Reservation(creator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reserved.Bytes, original.Bytes) || reserved.Attempt.Binding.Selection.Attempt != 2 ||
		checkpoint.CompletionDigest != "" || checkpoint.ManifestDigest != "" {
		t.Fatalf("before read changed original ordinal or invented completion evidence: %+v, %+v", reserved, checkpoint)
	}
	current.RunnerRevision = strings.Repeat("d", 40)
	if source, err := ReadBefore(context.Background(), nil, checkpoint, current); source != nil || !errors.Is(err, ErrIncompatible) {
		t.Fatalf("incompatible request accessed the owner before rejection: %+v, %v", source, err)
	}
	if digest, err := PublishBefore(context.Background(), nil, ledger, executionledger.Creator{}, request,
		checkpoint.Compatibility.RunnerRevision); digest != "" || !errors.Is(err, executionledger.ErrCapability) {
		t.Fatalf("readable source granted creator capability: %s, %v", digest, err)
	}
}

func TestBeforeCheckpointRejectsActualCompletedOriginal(t *testing.T) {
	checkpoint, backend, ledger, creator, manifest, terminal := beforeCheckpointSource(t)
	publishCheckpointSource(t, backend, manifest, terminal)
	digest := artifactmanifest.Hash(manifest)
	if _, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.Complete(context.Background(), creator, digest)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Reservation.Digest != checkpoint.ReservationDigest {
		t.Fatal("completion control did not complete the checkpoint's original reservation")
	}
	if source, err := ReadBefore(context.Background(), ledger, checkpoint, checkpoint.Compatibility); source != nil || !errors.Is(err, ErrStale) {
		t.Fatalf("stale before checkpoint admitted completed original work: %+v, %v", source, err)
	}
}

func TestBeforeCheckpointPresentCompletionNeverTreatsMissingEvidenceAsAbsent(t *testing.T) {
	for _, control := range []string{"missing-manifest", "unfinalized", "corrupt", "wrong-reservation", "wrong-key", "noncanonical"} {
		t.Run(control, func(t *testing.T) {
			checkpoint, backend, ledger, _, manifest, terminal := beforeCheckpointSource(t)
			record := executionledger.Completion{
				Kind: "waza.execution-completion", SchemaVersion: executionledger.Version,
				ExecutionID: checkpoint.ExecutionID, ReservationDigest: checkpoint.ReservationDigest,
				ManifestDigest: artifactmanifest.Hash(manifest),
			}
			if control == "unfinalized" {
				publishCheckpointSource(t, backend, manifest, terminal)
			}
			if control == "wrong-reservation" {
				record.ReservationDigest = strings.Repeat("e", 64)
			}
			if control == "wrong-key" {
				record.ExecutionID = "exec-" + strings.Repeat("f", 64)
			}
			data, err := executionledger.EncodeCompletion(record)
			if err != nil {
				t.Fatal(err)
			}
			if control == "corrupt" {
				data = []byte("corrupt-completion-control")
			}
			if control == "noncanonical" {
				data = append(data, '\n')
			}
			namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			if created, err := namespace.Create(context.Background(), "completion/"+checkpoint.ExecutionID, data); !created || err != nil {
				t.Fatalf("present completion control was not stored: %t, %v", created, err)
			}
			source, err := ReadBefore(context.Background(), ledger, checkpoint, checkpoint.Compatibility)
			if source != nil || err == nil || errors.Is(err, ErrStale) {
				t.Fatalf("unverified completion admitted fresh work or was labeled verified stale: %+v, %v", source, err)
			}
			if control == "missing-manifest" && !errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("missing referenced evidence control did not reach the real completed reader: %v", err)
			}
		})
	}
}

func TestBeforeCheckpointLateCompletionPreservesBlobButRejectsPublication(t *testing.T) {
	checkpoint, backend, ledger, creator, manifest, terminal := beforeCheckpointSource(t)
	publishCheckpointSource(t, backend, manifest, terminal)
	digest := artifactmanifest.Hash(manifest)
	if _, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	fault := &checkpointFaultBackend{Backend: backend}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := executionledger.EncodeCompletion(executionledger.Completion{
		Kind: "waza.execution-completion", SchemaVersion: executionledger.Version,
		ExecutionID: checkpoint.ExecutionID, ReservationDigest: checkpoint.ReservationDigest,
		ManifestDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := false
	fault.afterRead = func() {
		// A competing raw provider write bypasses the retained creator gate.
		// Reentrant Complete here would wait for this same Put to finish.
		if _, err := namespace.Create(context.Background(), "completion/"+checkpoint.ExecutionID, completion); err != nil {
			t.Fatal(err)
		}
		joined = true
	}
	stored, err := PublishBefore(context.Background(), fault, ledger, creator,
		readFixture(t, "../../examples/runner/unit.json"), checkpoint.Compatibility.RunnerRevision)
	if !joined || stored == "" || !errors.Is(err, executionledger.ErrContext) {
		t.Fatalf("late original completion lost its failure or residue locator: %t, %s, %v", joined, stored, err)
	}
	if blob, err := Load(context.Background(), backend, stored); blob == nil || err != nil {
		t.Fatalf("immutable checkpoint residue was deleted: %+v, %v", blob, err)
	}
}

func TestBeforePublicationUsesRetainedStateBeyondDiscoveryLimit(t *testing.T) {
	ctx := context.Background()
	checkpoint, binding, _, _, _, _ := checkpointContext(t, false)
	backend, _ := localCheckpointStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	for range artifactmanifest.MaxCatalogEntries + 1 {
		if _, err := ledger.Reserve(ctx, binding, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := readFixture(t, "../../examples/runner/unit.json")
	revision := checkpoint.Compatibility.RunnerRevision

	fresh, err := ledger.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := PublishBefore(ctx, backend, ledger, fresh, request, revision)
	if err != nil {
		t.Fatalf("fresh publication depended on bounded global discovery: %v", err)
	}
	before, err := Load(ctx, backend, digest)
	if err != nil {
		t.Fatal(err)
	}
	if source, err := ReadBefore(ctx, ledger, *before, before.Compatibility); source != nil || !errors.Is(err, ErrContext) {
		t.Fatalf("restore admitted unproven dispatch absence from truncated discovery: %+v, %v", source, err)
	}

	dispatched, err := ledger.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Distinct(ctx, dispatched); err != nil {
		t.Fatal(err)
	}
	if digest, err := PublishBefore(ctx, backend, ledger, dispatched, request, revision); digest != "" || !errors.Is(err, executionledger.ErrContext) {
		t.Fatalf("publication after a retained dispatch decision did not fail closed: %s, %v", digest, err)
	}
}
