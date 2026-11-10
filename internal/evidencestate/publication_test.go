package evidencestate

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

func publishOrigin(t *testing.T, backend artifactmanifest.Backend, origin artifactmanifest.Manifest, artifacts map[string][]byte) string {
	t.Helper()
	ctx := context.Background()
	store := artifactmanifest.NewStore(backend)
	for _, a := range origin.Artifacts {
		if err := store.Put(ctx, a, artifacts[a.Name]); err != nil {
			t.Fatal(err)
		}
	}
	data, err := artifactmanifest.Encode(origin)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.Create(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Finalize(ctx, digest)
	if err != nil || receipt.Publication != "finalized" || receipt.Integrity != "verified" {
		t.Fatalf("origin setup must actually finalize all bytes: %+v, %v", receipt, err)
	}
	return digest
}

func localStore(t *testing.T) (*artifactmanifest.Local, string) {
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
	return backend, directory
}

func TestPublicationUsesFinalizedCurrentSourcesAndSeparateBlob(t *testing.T) {
	backend, directory := localStore(t)
	d, _, artifacts, origin := attemptSourceFixture(t)
	publishedOrigin := publishOrigin(t, backend, origin, artifacts)
	if publishedOrigin != d.OriginManifest {
		t.Fatal("fixture source digest changed")
	}
	ctx := context.Background()
	original, err := backend.Get(ctx, "manifest-"+d.OriginManifest, artifactmanifest.MaxManifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Publish(ctx, backend, d)
	if err != nil || receipt.Integrity != "verified" || receipt.Publication != "derived" || receipt.Document == nil {
		t.Fatalf("real immutable publication failed: %+v, %v", receipt, err)
	}
	if len(receipt.Sources) != 1 || receipt.Sources[0].Publication != "finalized" ||
		receipt.Sources[0].Integrity != "verified" || len(receipt.Sources[0].Artifacts) != 0 {
		t.Fatal("source admission or privacy projection lost")
	}
	if _, err := os.Stat(filepath.Join(directory, "blob-"+receipt.EvidenceSHA256)); err != nil {
		t.Fatal("sidecar blob was not persisted")
	}
	if _, err := os.Stat(filepath.Join(directory, "final-"+receipt.EvidenceSHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sidecar invented a manifest final marker")
	}
	after, err := backend.Get(ctx, "manifest-"+d.OriginManifest, artifactmanifest.MaxManifestBytes)
	if err != nil || string(after) != string(original) {
		t.Fatal("derived publication modified origin bytes")
	}
	again, err := Publish(ctx, backend, d)
	if err != nil || again.EvidenceSHA256 != receipt.EvidenceSHA256 {
		t.Fatalf("idempotent byte publication failed: %+v, %v", again, err)
	}
	inspected, err := Inspect(ctx, backend, receipt.EvidenceSHA256)
	if err != nil || inspected.Document.ExecutionSource.ExecutionID != d.ExecutionSource.ExecutionID {
		t.Fatalf("actual persisted read lost source attempt: %+v, %v", inspected, err)
	}
	// The marker remains finalized, but corrupt current source bytes must fail.
	for _, a := range origin.Artifacts {
		if a.Name == "recorded" {
			path := filepath.Join(directory, "blob-"+a.SHA256)
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	inspected, err = Inspect(ctx, backend, receipt.EvidenceSHA256)
	if !errors.Is(err, artifactmanifest.ErrCorrupt) || inspected.Integrity != "failed" || inspected.Document != nil {
		t.Fatalf("stale finalized marker became evidence success: %+v, %v", inspected, err)
	}
	if len(inspected.Sources) != 0 {
		t.Fatal("failed completed read invented an admitted source receipt")
	}
	current, err := artifactmanifest.NewStore(backend).Inspect(ctx, d.OriginManifest)
	if !errors.Is(err, artifactmanifest.ErrCorrupt) || current.Publication != "finalized" || current.Integrity != "failed" {
		t.Fatal("fixture must retain finalization while current source integrity fails")
	}
}

func TestPublicationRejectsInvalidTerminalEvenWhenNotSelected(t *testing.T) {
	for _, control := range []string{"valid", "malformed", "binding_mismatch"} {
		t.Run(control, func(t *testing.T) {
			backend, directory := localStore(t)
			d, _, objects, origin := sourceFixture(t)
			original := objects[origin.TerminalArtifact]
			switch control {
			case "malformed":
				objects[origin.TerminalArtifact] = []byte("{")
			case "binding_mismatch":
				objects[origin.TerminalArtifact] = bytes.ReplaceAll(original, []byte(`"task":"task"`), []byte(`"task":"other"`))
			}
			terminal := objects[origin.TerminalArtifact]
			_, terminalErr := runnerterminal.Validate(terminal, origin.Binding.TerminalBinding())
			if control == "valid" {
				if terminalErr != nil {
					t.Fatal(terminalErr)
				}
			} else if bytes.Equal(original, terminal) || terminalErr == nil {
				t.Fatal("negative control must actually invalidate the original terminal")
			}
			for i := range origin.Artifacts {
				a := &origin.Artifacts[i]
				a.SHA256, a.SizeBytes = artifactmanifest.Hash(objects[a.Name]), int64(len(objects[a.Name]))
			}
			ctx := t.Context()
			store := artifactmanifest.NewStore(backend)
			for _, a := range origin.Artifacts {
				if err := store.Put(ctx, a, objects[a.Name]); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := artifactmanifest.Encode(origin)
			if err != nil {
				t.Fatal(err)
			}
			d.OriginManifest, err = store.Create(ctx, manifest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Finalize(ctx, d.OriginManifest)
			if control == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, artifactmanifest.ErrCorrupt) {
					t.Fatalf("native finalization must reject invalid terminal: %v", err)
				}
				// Model an invalid provider-side final marker, not a successful
				// producer finalization. Completed reads must still validate.
				if _, err := backend.Create(ctx, "final-"+d.OriginManifest, []byte(d.OriginManifest)); err != nil {
					t.Fatal(err)
				}
			}
			for i := range d.Categories {
				d.Categories[i].Artifacts = []string{"recorded"}
			}
			data, err := Encode(d)
			if err != nil {
				t.Fatal(err)
			}
			digest := artifactmanifest.Hash(data)
			assertReceipt := func(receipt Inspection, err error) {
				t.Helper()
				if control == "valid" {
					if err != nil || receipt.Integrity != "verified" || receipt.Document == nil {
						t.Fatalf("valid unselected terminal rejected: %+v, %v", receipt, err)
					}
				} else if !errors.Is(err, artifactmanifest.ErrCorrupt) || receipt.Integrity != "failed" || receipt.Document != nil {
					t.Fatalf("invalid unselected terminal admitted: %+v, %v", receipt, err)
				}
			}
			assertReceipt(Publish(ctx, backend, d))
			if control != "valid" {
				if _, err := os.Stat(filepath.Join(directory, "blob-"+digest)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected publication persisted a sidecar")
				}
				if err := store.Put(ctx, artifactmanifest.Artifact{
					Name: "execution-evidence", SHA256: digest, SizeBytes: int64(len(data)), MediaType: "application/json",
				}, data); err != nil {
					t.Fatal(err)
				}
			}
			assertReceipt(Inspect(ctx, backend, digest))
		})
	}
}

func TestPublicationWithholdsUnfinalizedAndMissingLineage(t *testing.T) {
	backend, directory := localStore(t)
	d, _, artifacts, origin := sourceFixture(t)
	ctx := context.Background()
	store := artifactmanifest.NewStore(backend)
	for _, a := range origin.Artifacts {
		if err := store.Put(ctx, a, artifacts[a.Name]); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := store.Create(ctx, wire(t, origin))
	if err != nil || digest != d.OriginManifest {
		t.Fatal("fixture must publish a valid but unfinalized origin")
	}
	evidenceDigest, err := Digest(d)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Publish(ctx, backend, d)
	if !errors.Is(err, artifactmanifest.ErrMissing) || receipt.Integrity != "failed" || receipt.Document != nil {
		t.Fatalf("unfinalized source admitted: %+v, %v", receipt, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "blob-"+evidenceDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sidecar persisted before source finalization")
	}
	if _, err := store.Finalize(ctx, digest); err != nil {
		t.Fatal(err)
	}
	d.Lineage = []Lineage{{Manifest: strings.Repeat("f", 64), Relationship: Resume}}
	receipt, err = Publish(ctx, backend, d)
	if !errors.Is(err, artifactmanifest.ErrMissing) || receipt.Document != nil {
		t.Fatalf("digest-shaped missing ancestor was trusted: %+v, %v", receipt, err)
	}
}

func TestPersistedCategoryDegradationDoesNotCreateAcceptance(t *testing.T) {
	for index, category := range sample().Categories {
		for _, state := range []State{Unavailable, Incomplete, Redacted, Conflicting} {
			t.Run(string(category.Category)+"/"+string(state), func(t *testing.T) {
				backend, _ := localStore(t)
				d, _, artifacts, origin := sourceFixture(t)
				r := &d.Categories[index]
				r.State, r.ObservedCount, r.Artifacts = state, nil, []string{}
				switch state {
				case Unavailable:
					r.Reason = NotRecorded
				case Incomplete:
					r.Reason = PartialCapture
				case Redacted:
					r.Reason = Privacy
				case Conflicting:
					r.Reason = SourceConflict
				}
				publishOrigin(t, backend, origin, artifacts)
				receipt, err := Publish(context.Background(), backend, d)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, record := range receipt.Document.Categories {
					if record.Category == category.Category {
						found = true
						if record.State != state || record.ObservedCount != nil || len(record.Artifacts) != 0 {
							t.Fatalf("persisted degradation changed: %+v", record)
						}
					}
				}
				if !found {
					t.Fatal("category disappeared")
				}
				data := wire(t, receipt)
				for _, forbidden := range []string{`"passed"`, `"ai_credits"`, `"value"`, "0.12345678901234567"} {
					if strings.Contains(string(data), forbidden) {
						t.Fatalf("receipt fabricated acceptance or leaked payload: %s", forbidden)
					}
				}
				if receipt.Verification != "content-and-declared-context-only" {
					t.Fatal("byte verification was presented as execution assurance")
				}
			})
		}
	}
}

func TestExplicitBackendSelectionAndInvalidDigest(t *testing.T) {
	for _, inputs := range [][2]string{{filepath.Join(t.TempDir(), "missing-store"), ""}, {"local", "not-an-https-url"}} {
		if backend, closeStore, err := artifactmanifest.OpenBackend(inputs[0], inputs[1]); err == nil || backend != nil || closeStore != nil {
			t.Fatal("invalid explicit backend selection was accepted")
		}
	}
	for _, digest := range []string{"", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if receipt, err := Inspect(context.Background(), nil, digest); !errors.Is(err, ErrInvalid) || receipt.Integrity != "failed" {
			t.Fatalf("invalid digest defaulted to storage success: %+v, %v", receipt, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	receipt, err := Publish(ctx, nil, sample())
	if err == nil || receipt.Document != nil || receipt.Integrity != "failed" {
		t.Fatalf("unavailable backend was accepted: %+v, %v", receipt, err)
	}
}
