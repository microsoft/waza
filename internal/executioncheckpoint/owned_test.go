package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func typedCheckpointSource(t *testing.T, failed bool) (Checkpoint, *artifactmanifest.Local,
	*executionledger.Ledger, executionledger.Creator, []byte, string,
) {
	t.Helper()
	checkpoint, binding, _, _, manifest, terminal := checkpointContext(t, failed)
	backend, directory := localCheckpointStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	// Actual production terminal producer + typed immutable owner/provider
	// admission, not proof of the pending single-admission submit/restore path.
	creator, err := ledger.Reserve(context.Background(), binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishCheckpointSource(t, backend, manifest, terminal)
	if _, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), checkpoint.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.Complete(context.Background(), creator, checkpoint.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.ExecutionID = completed.Reservation.Attempt.ExecutionID
	checkpoint.ReservationDigest = completed.Reservation.Digest
	checkpoint.CompletionDigest = artifactmanifest.Hash(completed.CompletionBytes)
	return checkpoint, backend, ledger, creator, terminal, directory
}

func TestOwnedFinalizedCheckpointUsesRetainedCreatorAndOriginalStoredTerminal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "passed", true: "failed-checks"}[failed], func(t *testing.T) {
			checkpoint, backend, ledger, creator, terminal, _ := typedCheckpointSource(t, failed)
			request := readFixture(t, "../../examples/runner/unit.json")
			if failed {
				request = bytes.Replace(request, []byte("- hello"), []byte("- impossible-checkpoint-control"), 1)
			}
			digest, err := PublishFinalized(context.Background(), backend, ledger, creator, request,
				checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(context.Background(), backend, digest)
			if err != nil || loaded == nil {
				t.Fatalf("owned checkpoint blob did not load: %+v, %v", loaded, err)
			}
			created, err := time.Parse(time.RFC3339Nano, loaded.CreatedAt)
			if err != nil || created.IsZero() || created.Format(time.RFC3339Nano) != loaded.CreatedAt {
				t.Fatalf("creation did not record a canonical timestamp: %s, %v", loaded.CreatedAt, err)
			}
			comparison := *loaded
			comparison.CreatedAt = checkpoint.CreatedAt
			if comparison != checkpoint {
				t.Fatalf("owned creation did not retain exact original context: %+v", loaded)
			}
			source, err := ReadStoredFinalized(context.Background(), ledger, *loaded, loaded.Compatibility)
			if err != nil || source == nil {
				t.Fatalf("typed source did not verify: %+v, %v", source, err)
			}
			if !bytes.Equal(source.Publication.ArtifactBytes[source.Publication.Manifest.TerminalArtifact], terminal) {
				t.Fatal("source terminal was regenerated or retagged")
			}
			result, err := runnerterminal.Validate(terminal, source.Reservation.Attempt.Binding.TerminalBinding())
			expected := runnerterminal.Success
			if failed {
				expected = runnerterminal.EvaluationFailure
			}
			if err != nil || result.Outcome != expected {
				t.Fatalf("original terminal outcome was not retained: %+v, %v", result, err)
			}
		})
	}
}

func TestOwnedFinalizedCheckpointRejectsForeignCreatorAndRequestRetag(t *testing.T) {
	checkpoint, backend, ledger, creator, _, _ := typedCheckpointSource(t, false)
	request := readFixture(t, "../../examples/runner/unit.json")
	foreign, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []*executionledger.Ledger{nil, foreign} {
		if digest, err := PublishFinalized(context.Background(), backend, owner, creator, request,
			checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest); digest != "" || !errors.Is(err, executionledger.ErrCapability) {
			t.Fatalf("foreign/nil owner published a checkpoint: %s, %v", digest, err)
		}
	}
	if digest, err := PublishFinalized(context.Background(), backend, ledger, executionledger.Creator{}, request,
		checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest); digest != "" || !errors.Is(err, executionledger.ErrCapability) {
		t.Fatalf("zero creator published a checkpoint: %s, %v", digest, err)
	}
	modified := bytes.Replace(request, []byte(`"attempt": 2`), []byte(`"attempt": 3`), 1)
	if bytes.Equal(request, modified) {
		t.Fatal("native ordinal control did not change original request")
	}
	if digest, err := PublishFinalized(context.Background(), backend, ledger, creator, modified,
		checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest); digest != "" || !errors.Is(err, ErrContext) {
		t.Fatalf("new caller ordinal replaced original request: %s, %v", digest, err)
	}
	if digest, err := PublishFinalized(context.Background(), backend, ledger, creator, request,
		"", checkpoint.ManifestDigest); digest != "" || !errors.Is(err, ErrIncompatible) {
		t.Fatalf("unstamped creation became valid: %s, %v", digest, err)
	}
	fault := &checkpointFaultBackend{Backend: backend, createErr: errors.New("checkpoint write control")}
	digest, err := PublishFinalized(context.Background(), fault, ledger, creator, request,
		checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest)
	if digest == "" || err == nil || fault.calls == 0 {
		t.Fatalf("failed blob publication lost failure or residue locator: %s, %v", digest, err)
	}
}

func TestOwnedFinalizedCheckpointCreatorFailureRetainsPhaseContextAndCause(t *testing.T) {
	checkpoint, backend, ledger, creator, _, _ := typedCheckpointSource(t, false)
	request := readFixture(t, "../../examples/runner/unit.json")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer stop()
	for _, control := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
	} {
		t.Run(control.name, func(t *testing.T) {
			digest, err := PublishFinalized(control.ctx, backend, ledger, creator, request,
				checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest)
			if digest != "" || !errors.Is(err, control.want) {
				t.Fatalf("creator failure lost context classification or published a checkpoint: %s %v", digest, err)
			}
			for _, phase := range []string{
				"request_declaration:", "compatibility_elapsed=",
				"request_deadline_remaining_at_entry=", "request_entry_ctx_err=" + control.want.Error(),
				"request_compatibility_ctx_err=" + control.want.Error(),
				"creator_publication:", "creator_declaration_elapsed=", "retained_elapsed=",
				"selection_elapsed=", "identity_elapsed=", "creator_validation_elapsed=",
				"deadline_remaining_at_entry=", "entry_ctx_err=" + control.want.Error(),
				"selection_ctx_err=" + control.want.Error(), "identity_ctx_err=" + control.want.Error(),
				"final_ctx_err=" + control.want.Error(),
			} {
				if !strings.Contains(err.Error(), phase) {
					t.Fatalf("creator failure omitted phase/context %q: %v", phase, err)
				}
			}
		})
	}
}

func TestCreatorFailureSeparatesRequestAndPublicationContexts(t *testing.T) {
	checkpoint, backend, ledger, creator, _, _ := typedCheckpointSource(t, false)
	request := readFixture(t, "../../examples/runner/unit.json")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expiredPublication, stopExpired := context.WithDeadline(context.WithoutCancel(canceled), time.Unix(1, 0))
	defer stopExpired()
	live, stopLive := context.WithTimeout(t.Context(), time.Hour)
	defer stopLive()
	canceledPublication, cancelPublication := context.WithCancel(context.WithoutCancel(live))
	cancelPublication()

	for _, control := range []struct {
		name        string
		requestCtx  context.Context
		publication context.Context
		requestErr  string
		want        error
	}{
		{"canceled-request-expired-publication", canceled, expiredPublication, context.Canceled.Error(), context.DeadlineExceeded},
		{"live-request-canceled-publication", live, canceledPublication, "<nil>", context.Canceled},
		{"unobserved-request", nil, canceledPublication, "unavailable", context.Canceled},
	} {
		t.Run(control.name, func(t *testing.T) {
			var declaration RequestDeclaration
			var err error
			if control.requestCtx == nil {
				declaration, err = DeclareRequest(request, checkpoint.Compatibility.RunnerRevision)
			} else {
				declaration, err = DeclareRequestWithContext(control.requestCtx, request, checkpoint.Compatibility.RunnerRevision)
			}
			if err != nil {
				t.Fatal(err)
			}
			if control.name == "live-request-canceled-publication" {
				remaining, err := time.ParseDuration(declaration.diagnostics.deadlineRemaining)
				if err != nil || remaining <= 0 {
					t.Fatalf("request deadline observation is not live: %q, %v", declaration.diagnostics.deadlineRemaining, err)
				}
			}
			for _, boundary := range []string{BeforeUnit, FinalizedUnit} {
				var digest string
				if boundary == BeforeUnit {
					digest, err = PublishBeforeDeclared(control.publication, backend, ledger, creator, declaration)
				} else {
					digest, err = PublishFinalizedDeclared(control.publication, backend, ledger, creator, declaration, checkpoint.ManifestDigest)
				}
				if digest != "" || !errors.Is(err, control.want) {
					t.Fatalf("%s creator failure lost its publication cause: %s %v", boundary, digest, err)
				}
				requestPhase, creatorPhase, ok := strings.Cut(err.Error(), "; creator_publication:")
				if !ok {
					t.Fatalf("%s omitted separately labeled phases: %v", boundary, err)
				}
				for _, field := range []string{
					"compatibility_elapsed=" + declaration.diagnostics.elapsed.String(),
					"request_deadline_remaining_at_entry=" + declaration.diagnostics.deadlineRemaining,
					"request_entry_ctx_err=" + control.requestErr,
					"request_compatibility_ctx_err=" + control.requestErr,
				} {
					if !strings.Contains(requestPhase, field) {
						t.Fatalf("%s omitted original request observation %q: %v", boundary, field, err)
					}
				}
				for _, field := range []string{
					"entry_ctx_err=" + control.want.Error(),
					"selection_ctx_err=" + control.want.Error(),
					"identity_ctx_err=" + control.want.Error(),
					"final_ctx_err=" + control.want.Error(),
				} {
					if !strings.Contains(creatorPhase, field) {
						t.Fatalf("%s conflated publication observation %q: %v", boundary, field, err)
					}
				}
			}
		})
	}
}

func TestTypedFinalizedCheckpointRejectsSourceMismatchAndCurrentCorruption(t *testing.T) {
	checkpoint, _, ledger, _, _, directory := typedCheckpointSource(t, false)
	current := checkpoint.Compatibility
	current.RunnerRevision = strings.Repeat("e", 40)
	if source, err := ReadStoredFinalized(context.Background(), nil, checkpoint, current); source != nil || !errors.Is(err, ErrIncompatible) {
		t.Fatalf("compatibility was not rejected before ledger access: %+v, %v", source, err)
	}
	if source, err := ReadStoredFinalized(context.Background(), nil, checkpoint, checkpoint.Compatibility); source != nil || !errors.Is(err, executionledger.ErrCapability) {
		t.Fatalf("absent typed source became valid: %+v, %v", source, err)
	}
	for _, edit := range []func(*Checkpoint){
		func(c *Checkpoint) { c.ReservationDigest = strings.Repeat("0", 64) },
		func(c *Checkpoint) { c.CompletionDigest = strings.Repeat("0", 64) },
		func(c *Checkpoint) { c.Boundary, c.CompletionDigest, c.ManifestDigest = BeforeUnit, "", "" },
	} {
		value := checkpoint
		edit(&value)
		if source, err := ReadStoredFinalized(context.Background(), ledger, value, value.Compatibility); source != nil || !errors.Is(err, ErrContext) {
			t.Fatalf("typed source mismatch passed: %+v, %v", source, err)
		}
	}

	source, err := ReadStoredFinalized(context.Background(), ledger, checkpoint, checkpoint.Compatibility)
	if err != nil || source == nil {
		t.Fatalf("positive finalized source control failed: %+v, %v", source, err)
	}
	path := filepath.Join(directory, "blob-"+source.Publication.Manifest.Artifacts[0].SHA256)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("actual-current-source-corruption"), 0o600); err != nil {
		t.Fatal(err)
	}
	if source, err := ReadStoredFinalized(context.Background(), ledger, checkpoint, checkpoint.Compatibility); source != nil ||
		!errors.Is(err, artifactmanifest.ErrCorrupt) {
		t.Fatalf("typed reader reused a previously verified source instead of current bytes: %+v, %v", source, err)
	}
}

func TestOwnedCheckpointPostWriteCreatorFailurePreservesUnverifiedBlob(t *testing.T) {
	checkpoint, backend, ledger, creator, _, directory := typedCheckpointSource(t, false)
	request := readFixture(t, "../../examples/runner/unit.json")
	reserved, err := ledger.Reservation(creator)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "execution-ledger-v1-reservation-"+reserved.Attempt.ExecutionID)
	fault := &checkpointFaultBackend{Backend: backend}
	changed := false
	fault.afterRead = func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("external-corrupt-reservation-control"), 0o600); err != nil {
			t.Fatal(err)
		}
		changed = true
	}
	digest, err := PublishFinalized(context.Background(), fault, ledger, creator, request,
		checkpoint.Compatibility.RunnerRevision, checkpoint.ManifestDigest)
	if !changed || digest == "" || err == nil || !strings.Contains(err.Error(), "stored but original creator validation failed") {
		t.Fatalf("post-write creator failure was not propagated with a residue locator: %t, %s, %v", changed, digest, err)
	}
	loaded, err := Load(context.Background(), backend, digest)
	if err != nil || loaded == nil {
		t.Fatalf("invocation-owned stored evidence was removed after a late failure: %+v, %v", loaded, err)
	}
	if source, err := ReadStoredFinalized(context.Background(), ledger, *loaded, loaded.Compatibility); source != nil || err == nil {
		t.Fatalf("existing valid blob bypassed corrupt original source admission: %+v, %v", source, err)
	}
}

func TestOwnedCheckpointRejectsCoherentSelectionIdentityMismatch(t *testing.T) {
	checkpoint, binding, _, _, manifest, terminal := checkpointContext(t, false)
	original, err := runnerterminal.Validate(terminal, binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.Selection.Attempt++
	if wrong.Selection == binding.Selection || wrong.Identity != binding.Identity {
		t.Fatal("negative control must change only the declared native tuple, not the request-derived identity")
	}
	original.Selection = &wrong.Selection
	original.Observation.Selection = wrong.Selection
	original.Observation.Attempts = wrong.Selection.Attempt
	retagged, err := runnerterminal.Encode(*original, wrong.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := artifactmanifest.Parse(manifest)
	if err != nil {
		t.Fatal(err)
	}
	declaration.Binding = wrong
	declaration.Artifacts[0].SHA256 = artifactmanifest.Hash(retagged)
	declaration.Artifacts[0].SizeBytes = int64(len(retagged))
	manifest, err = artifactmanifest.Encode(*declaration)
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := localCheckpointStore(t)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(context.Background(), wrong, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishCheckpointSource(t, backend, manifest, retagged)
	manifestDigest := artifactmanifest.Hash(manifest)
	if _, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), manifestDigest); err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.Complete(context.Background(), creator, manifestDigest)
	if err != nil || completed == nil {
		t.Fatalf("coherent negative control must pass declared owner/artifact admission: %+v, %v", completed, err)
	}
	request := readFixture(t, "../../examples/runner/unit.json")
	if digest, err := PublishFinalized(context.Background(), backend, ledger, creator, request,
		checkpoint.Compatibility.RunnerRevision, manifestDigest); digest != "" || !errors.Is(err, ErrContext) {
		t.Fatalf("checkpoint creation substituted a source tuple inconsistent with the original request: %s, %v", digest, err)
	}
}
