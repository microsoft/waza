package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnerterminal"
)

type completedReadBackend struct {
	Backend
	reads     map[string]int
	nilOnZero bool
}

func (b *completedReadBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.reads[key]++
	data, err := b.Backend.Get(ctx, key, limit)
	if b.nilOnZero && len(data) == 0 {
		data = nil
	}
	return data, err
}

func completedFixture(t *testing.T, backend Backend, m Manifest, terminal []byte) (string, []byte) {
	t.Helper()
	wire, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(backend)
	digest, err := store.Create(context.Background(), wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.Artifacts {
		var data []byte
		if a.Name == m.TerminalArtifact {
			data = terminal
		}
		if err := store.Put(context.Background(), a, data); err != nil {
			t.Fatal(err)
		}
	}
	return digest, wire
}

func TestReadCompletedRetainsSamePassAndDetachedBytes(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			backend, _, _ := namespaceFixture(t, provider)
			m, terminal := publicationFixture(t)
			m.Artifacts = append(m.Artifacts, Artifact{Name: "empty", SHA256: Hash(nil), MediaType: "application/octet-stream"})
			digest, wire := completedFixture(t, backend, m, terminal)
			store := NewStore(backend)
			if receipt, err := store.Inspect(context.Background(), digest); err != nil || receipt.Publication != "unfinalized" || receipt.Integrity != "verified" {
				t.Fatalf("legacy unfinalized inspect changed: %+v %v", receipt, err)
			}
			if got, err := store.ReadCompleted(context.Background(), digest); got != nil || !errors.Is(err, ErrMissing) {
				t.Fatalf("unfinalized declaration became completed bytes: %+v %v", got, err)
			}
			if _, err := store.Finalize(context.Background(), digest); err != nil {
				t.Fatal(err)
			}
			counted := &completedReadBackend{Backend: backend, reads: make(map[string]int)}
			completed, err := NewStore(counted).ReadCompleted(context.Background(), digest)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(completed.ManifestBytes, wire) || Hash(completed.ManifestBytes) != digest ||
				!bytes.Equal(completed.ArtifactBytes[m.TerminalArtifact], terminal) || len(completed.ArtifactBytes) != 1 {
				t.Fatalf("completed bytes were repaired, incomplete or overcaptured: %+v", completed)
			}
			for _, key := range []string{"manifest-" + digest, "final-" + digest, "blob-" + Hash(terminal), "blob-" + Hash(nil)} {
				if counted.reads[key] != 1 {
					t.Fatalf("verification reopened %s %d times", key, counted.reads[key])
				}
			}
			completed.ManifestBytes[0] = 'X'
			completed.ArtifactBytes[m.TerminalArtifact][0] = 'Y'
			completed.Manifest.Binding.GraderIDs[0] = "changed"
			completed.Manifest.Artifacts[0].Name = "changed"
			later, err := store.ReadCompleted(context.Background(), digest)
			if err != nil || !bytes.Equal(later.ManifestBytes, wire) || !bytes.Equal(later.ArtifactBytes[m.TerminalArtifact], terminal) {
				t.Fatalf("returned data mutated store or later read: %+v %v", later, err)
			}
			for _, names := range [][]string{{"missing"}, {m.TerminalArtifact, m.TerminalArtifact}} {
				if got, err := store.ReadCompleted(context.Background(), digest, names...); got != nil || !errors.Is(err, ErrInvalid) {
					t.Fatalf("unknown/duplicate selection admitted: %v %v", got, err)
				}
			}
			all, err := store.ReadCompleted(context.Background(), digest, m.TerminalArtifact, "empty")
			if err != nil || len(all.ArtifactBytes) != 2 {
				t.Fatalf("explicit zero-byte artifact lost: %+v %v", all, err)
			}
			if data, found := all.ArtifactBytes["empty"]; !found || len(data) != 0 {
				t.Fatal("zero-byte artifact was treated as absent")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if got, err := store.ReadCompleted(ctx, digest); got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read returned completed data: %+v %v", got, err)
			}
		})
	}
}

func TestReadCompletedPreservesValidFailureAndCancellation(t *testing.T) {
	for _, outcome := range []runnerterminal.Outcome{runnerterminal.InfrastructureFailure, runnerterminal.Canceled} {
		t.Run(string(outcome), func(t *testing.T) {
			backend, _, _ := namespaceFixture(t, "local")
			m, original := publicationFixture(t)
			envelope, err := runnerterminal.Validate(original, m.Binding.TerminalBinding())
			if err != nil {
				t.Fatal(err)
			}
			envelope.Outcome = outcome
			envelope.ExitCode, _ = outcome.ExitCode()
			envelope.FailureBoundary = "shutdown"
			envelope.Diagnostics = []runnerterminal.Diagnostic{{Code: "shutdown", Message: "Shutdown did not complete."}}
			terminal, err := runnerterminal.Encode(*envelope, m.Binding.TerminalBinding())
			if err != nil {
				t.Fatal(err)
			}
			m.Artifacts[0].SHA256, m.Artifacts[0].SizeBytes = Hash(terminal), int64(len(terminal))
			digest, _ := completedFixture(t, backend, m, terminal)
			store := NewStore(backend)
			if _, err := store.Finalize(context.Background(), digest); err != nil {
				t.Fatal(err)
			}
			completed, err := store.ReadCompleted(context.Background(), digest)
			if err != nil || !bytes.Equal(completed.ArtifactBytes[m.TerminalArtifact], terminal) {
				t.Fatalf("valid recorded non-success was rewritten/rejected: %+v %v", completed, err)
			}
		})
	}
}

func TestReadCompletedRejectsCurrentReplacementAndMarkerConflict(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		for _, control := range []string{"corrupt", "oversize", "missing", "marker"} {
			t.Run(provider+"/"+control, func(t *testing.T) {
				backend, _, transport := namespaceFixture(t, provider)
				m, terminal := publicationFixture(t)
				digest, _ := completedFixture(t, backend, m, terminal)
				store := NewStore(backend)
				if _, err := store.Finalize(context.Background(), digest); err != nil {
					t.Fatal(err)
				}
				if _, err := store.ReadCompleted(context.Background(), digest); err != nil {
					t.Fatal(err)
				}
				key, replacement := "blob-"+Hash(terminal), bytes.Clone(terminal)
				expected := ErrCorrupt
				switch control {
				case "corrupt":
					replacement[0] = 'X'
				case "oversize":
					replacement = append(replacement, ' ')
				case "missing":
					expected = ErrMissing
				case "marker":
					key, replacement, expected = "final-"+digest, []byte(strings.Repeat("f", 64)), ErrConflict
				}
				if local, ok := backend.(*Local); ok {
					if err := local.root.Remove(key); err != nil {
						t.Fatal(err)
					}
					if control != "missing" {
						if err := local.root.WriteFile(key, replacement, 0o400); err != nil {
							t.Fatal(err)
						}
					}
				} else if control == "missing" {
					delete(transport.objects, key)
				} else {
					transport.objects[key] = replacement
				}
				if got, err := store.ReadCompleted(context.Background(), digest); got != nil || !errors.Is(err, expected) {
					t.Fatalf("current invalid bytes became completed evidence: %+v %v", got, err)
				}
			})
		}
	}
}

func TestReadCompletedRejectsNilEmptyTerminal(t *testing.T) {
	backend, _, _ := namespaceFixture(t, "local")
	m, _ := publicationFixture(t)
	m.Artifacts[0].SHA256, m.Artifacts[0].SizeBytes = Hash(nil), 0
	digest, _ := completedFixture(t, backend, m, nil)
	counted := &completedReadBackend{Backend: backend, reads: make(map[string]int), nilOnZero: true}
	store := NewStore(counted)
	if receipt, err := store.Inspect(context.Background(), digest); err == nil || receipt.Integrity != "failed" {
		t.Fatalf("nil zero-byte terminal bypassed native validation: %+v %v", receipt, err)
	}

	if got, err := store.ReadCompleted(context.Background(), digest); got != nil || err == nil {
		t.Fatalf("nil terminal became completed evidence: %+v %v", got, err)
	}
}

func TestReadCompletedNativeTerminalByteBoundary(t *testing.T) {
	backend, _, _ := namespaceFixture(t, "local")
	m, original := publicationFixture(t)
	terminal := append(bytes.Clone(original), bytes.Repeat([]byte(" "), runnerterminal.MaxEnvelopeBytes-len(original))...)
	if len(terminal) != runnerterminal.MaxEnvelopeBytes {
		t.Fatal("boundary control did not produce the exact native limit")
	}
	m.Artifacts[0].SHA256, m.Artifacts[0].SizeBytes = Hash(terminal), int64(len(terminal))
	digest, _ := completedFixture(t, backend, m, terminal)
	store := NewStore(backend)
	if _, err := store.Finalize(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ReadCompleted(context.Background(), digest)
	if err != nil || !bytes.Equal(completed.ArtifactBytes[m.TerminalArtifact], terminal) {
		t.Fatalf("native boundary bytes were rejected or normalized: %v", err)
	}
	m.Artifacts[0].SizeBytes++
	if _, err := Encode(m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("terminal metadata above native bound admitted: %v", err)
	}
}
