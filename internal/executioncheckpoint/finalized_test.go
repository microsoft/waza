package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func publishCheckpointSource(t *testing.T, backend artifactmanifest.Backend, manifest, terminal []byte) {
	t.Helper()
	value, err := artifactmanifest.Parse(manifest)
	if err != nil {
		t.Fatal(err)
	}
	store := artifactmanifest.NewStore(backend)
	digest, err := store.Create(context.Background(), manifest)
	if err != nil || digest != artifactmanifest.Hash(manifest) {
		t.Fatalf("source declaration did not retain manifest bytes: %s, %v", digest, err)
	}
	for _, artifact := range value.Artifacts {
		if artifact.Name != value.TerminalArtifact {
			t.Fatal("source fixture has an unexpected nonterminal artifact")
		}
		if err := store.Put(context.Background(), artifact, terminal); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFinalizedCheckpointReadsOriginalProducerStatuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failed  bool
		cancel  bool
		outcome runnerterminal.Outcome
	}{
		{"passed", false, false, runnerterminal.Success},
		{"failed-checks", true, false, runnerterminal.EvaluationFailure},
		{"canceled-before-execution", false, true, runnerterminal.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			checkpoint, binding, reservation, completion, manifest, terminal := checkpointContextIn(t, ctx, tc.failed)
			backend, _ := localCheckpointStore(t)
			publishCheckpointSource(t, backend, manifest, terminal)
			read := func() (*artifactmanifest.CompletedPublication, error) {
				return ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, binding, reservation, completion)
			}
			if value, err := read(); value != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("unfinalized source became resumable: %+v, %v", value, err)
			}
			receipt, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), checkpoint.ManifestDigest)
			if err != nil || receipt.Publication != "finalized" || receipt.Integrity != "verified" {
				t.Fatalf("actual source did not finalize: %+v, %v", receipt, err)
			}
			digest, err := Put(context.Background(), backend, checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(context.Background(), backend, digest)
			if err != nil || loaded == nil {
				t.Fatalf("actual checkpoint declaration did not load: %+v, %v", loaded, err)
			}
			publication, err := ReadFinalized(context.Background(), backend, *loaded, checkpoint.Compatibility, binding, reservation, completion)
			if err != nil || publication == nil {
				t.Fatalf("actual finalized source not returned: %+v, %v", publication, err)
			}
			got := publication.ArtifactBytes[publication.Manifest.TerminalArtifact]
			if !bytes.Equal(got, terminal) || !bytes.Equal(publication.ManifestBytes, manifest) {
				t.Fatal("source bytes were retagged or status was regenerated")
			}
			envelope, err := runnerterminal.Validate(got, binding.TerminalBinding())
			if err != nil || envelope.Outcome != tc.outcome || envelope.Identity == nil ||
				*envelope.Identity != binding.Identity || envelope.Selection == nil || *envelope.Selection != binding.Selection {
				t.Fatalf("producer status/identity/ordinal changed: %+v, %v", envelope, err)
			}
		})
	}
}

func TestFinalizedCheckpointRejectsCompatibilityContextAndCurrentCorruption(t *testing.T) {
	checkpoint, binding, reservation, completion, manifest, terminal := checkpointContext(t, false)
	backend, directory := localCheckpointStore(t)
	publishCheckpointSource(t, backend, manifest, terminal)
	if _, err := artifactmanifest.NewStore(backend).Finalize(context.Background(), checkpoint.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	fault := &checkpointFaultBackend{Backend: backend}
	current := checkpoint.Compatibility
	current.RunnerRevision = strings.Repeat("e", 40)
	if value, err := ReadFinalized(context.Background(), fault, checkpoint, current, binding, reservation, completion); value != nil ||
		!errors.Is(err, ErrIncompatible) || fault.calls != 0 {
		t.Fatalf("incompatible source read storage or succeeded: %+v, %v, %d", value, err, fault.calls)
	}
	before := checkpoint
	before.Boundary, before.CompletionDigest, before.ManifestDigest = BeforeUnit, "", ""
	if value, err := ReadFinalized(context.Background(), fault, before, before.Compatibility, binding, reservation, nil); value != nil ||
		!errors.Is(err, ErrContext) || fault.calls != 0 {
		t.Fatalf("before-unit read completed bytes: %+v, %v, %d", value, err, fault.calls)
	}
	wrong := binding
	wrong.Selection.Attempt++
	if value, err := ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, wrong, reservation, completion); value != nil ||
		!errors.Is(err, ErrContext) {
		t.Fatalf("new caller ordinal substituted for original source: %+v, %v", value, err)
	}
	if value, err := ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, binding, nil, completion); value != nil ||
		!errors.Is(err, ErrContext) {
		t.Fatalf("missing original reservation accepted: %+v, %v", value, err)
	}
	if value, err := ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, binding, reservation, nil); value != nil ||
		!errors.Is(err, ErrContext) {
		t.Fatalf("missing original completion accepted: %+v, %v", value, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := ReadFinalized(canceled, backend, checkpoint, checkpoint.Compatibility, binding, reservation, completion); value != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source read became success: %+v, %v", value, err)
	}
	// A previous verified checkpoint or manifest receipt cannot bypass rereading
	// current source bytes. A corrupt external replacement is never repaired.
	if value, err := ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, binding, reservation, completion); err != nil || value == nil {
		t.Fatalf("positive source control failed: %+v, %v", value, err)
	}
	path := filepath.Join(directory, "blob-"+artifactmanifest.Hash(terminal))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt-source-control"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := ReadFinalized(context.Background(), backend, checkpoint, checkpoint.Compatibility, binding, reservation, completion); value != nil ||
		!errors.Is(err, artifactmanifest.ErrCorrupt) {
		t.Fatalf("stale verified receipt hid current corruption: %+v, %v", value, err)
	}
}
