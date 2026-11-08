package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPublicationCatalogTruncationAndCancellation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			var backend Backend
			var insert func(string)
			if provider == "local" {
				local, err := OpenLocal(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := local.Close(); err != nil {
						t.Error(err)
					}
				})
				backend = local
				insert = func(key string) {
					t.Helper()
					if err := local.root.WriteFile(key, []byte("unverified declaration"), 0o400); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				azure, transport := azureFixture(t)
				backend = azure
				insert = func(key string) { transport.objects[key] = []byte("unverified declaration") }
			}
			for i := range MaxCatalogEntries + 1 {
				insert(fmt.Sprintf("manifest-%064x", i))
			}
			store := NewStore(backend)
			catalog, err := store.Catalog(context.Background())
			if err != nil || len(catalog.ManifestSHA256) != MaxCatalogEntries || !catalog.Truncated ||
				catalog.Verification != "unverified-pointers-only" || !slices.IsSorted(catalog.ManifestSHA256) {
				t.Fatalf("bounded unverified catalog lost contract: count=%d %+v %v", len(catalog.ManifestSHA256), catalog, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			catalog, err = store.Catalog(ctx)
			if !errors.Is(err, context.Canceled) || len(catalog.ManifestSHA256) != 0 || len(catalog.Diagnostics) == 0 {
				t.Fatalf("canceled discovery became success: %+v %v", catalog, err)
			}
		})
	}
}

func TestLocalPrimitiveDoesNotAcceptReplacedStagingBytes(t *testing.T) {
	local, err := OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := local.Close(); err != nil {
			t.Error(err)
		}
	})
	data := []byte("owned content")
	var stage string
	local.beforeLink = func(name, key string) error {
		stage = name
		if err := local.root.Rename(name, name+"-owned"); err != nil {
			return err
		}
		return local.root.WriteFile(name, []byte("replacement"), 0o600)
	}
	created, err := local.Create(context.Background(), "blob-"+Hash(data), data)
	if created || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("primitive accepted replaced stage: created=%v err=%v", created, err)
	}
	got, err := local.root.ReadFile(stage)
	if err != nil || string(got) != "replacement" {
		t.Fatalf("competitor removed or changed: %q %v", got, err)
	}
	got, err = local.root.ReadFile(stage + "-owned")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("owned displaced content lost: %q %v", got, err)
	}
}

type ambiguousCommit struct {
	Backend
	key  string
	fail bool
}

type markerReadFailure struct {
	Backend
	key string
	err error
}

func (b markerReadFailure) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if key == b.key {
		return nil, b.err
	}
	return b.Backend.Get(ctx, key, limit)
}

type missingWithoutCauses struct{}

func (missingWithoutCauses) Error() string   { return "missing without observed causes" }
func (missingWithoutCauses) Is(e error) bool { return e == ErrMissing }
func (missingWithoutCauses) Unwrap() []error { return nil }

func TestPublicationMarkerObservation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		for _, scenario := range []struct {
			name        string
			marker      string
			readErr     error
			removeInput bool
			publication string
			success     bool
		}{
			{name: "matching", publication: "finalized", success: true},
			{name: "absent", marker: "absent", publication: "unfinalized", success: true},
			{name: "conflict", marker: strings.Repeat("f", 64), publication: "conflict"},
			{name: "malformed", marker: "malformed", publication: "unknown"},
			{name: "unreadable", readErr: os.ErrPermission, publication: "unknown"},
			{name: "canceled", readErr: context.Canceled, publication: "unknown"},
			{name: "mixed-missing", readErr: errors.Join(ErrMissing, os.ErrPermission), publication: "unknown"},
			{name: "nested-mixed", readErr: fmt.Errorf("read marker: %w", errors.Join(ErrMissing, ErrConflict)), publication: "unknown"},
			{name: "empty-missing-causes", readErr: missingWithoutCauses{}, publication: "unknown"},
			{name: "wrapped-missing", marker: "absent", readErr: fmt.Errorf("read marker: %w", ErrMissing), publication: "unfinalized", success: true},
			{name: "joined-missing", marker: "absent", readErr: errors.Join(ErrMissing, fmt.Errorf("read marker: %w", ErrMissing)), publication: "unfinalized", success: true},
			{name: "missing-input-and-unreadable-marker", readErr: os.ErrPermission, removeInput: true, publication: "unknown"},
			{name: "missing-input-and-matching-marker", removeInput: true, publication: "finalized"},
		} {
			t.Run(provider+"/"+scenario.name, func(t *testing.T) {
				var backend Backend
				var azureTransport *fakeAzure
				var replace func(string, []byte)
				if provider == "local" {
					local, err := OpenLocal(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := local.Close(); err != nil {
							t.Error(err)
						}
					})
					backend = local
					replace = func(key string, data []byte) {
						t.Helper()
						if err := local.root.Chmod(key, 0o600); err != nil {
							t.Fatal(err)
						}
						if err := local.root.Remove(key); err != nil {
							t.Fatal(err)
						}
						if data != nil {
							if err := local.root.WriteFile(key, data, 0o400); err != nil {
								t.Fatal(err)
							}
						}
					}
				} else {
					azure, transport := azureFixture(t)
					backend = azure
					azureTransport = transport
					replace = func(key string, data []byte) {
						transport.mu.Lock()
						defer transport.mu.Unlock()
						if _, exists := transport.objects[key]; !exists {
							t.Fatalf("control object %s was not published", key)
						}
						if data == nil {
							delete(transport.objects, key)
						} else {
							transport.objects[key] = bytes.Clone(data)
						}
					}
				}
				m, terminal := publicationFixture(t)
				wire, err := Encode(m)
				if err != nil {
					t.Fatal(err)
				}
				store := NewStore(backend)
				digest, err := store.Create(context.Background(), wire)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Put(context.Background(), m.Artifacts[0], terminal); err != nil {
					t.Fatal(err)
				}
				before, err := store.Finalize(context.Background(), digest)
				if err != nil || before.Publication != "finalized" || before.Integrity != "verified" {
					t.Fatalf("control did not publish actual finalized bytes: %+v %v", before, err)
				}
				key := "final-" + digest
				if scenario.marker == "absent" {
					replace(key, nil)
				} else if scenario.marker != "" {
					replace(key, []byte(scenario.marker))
				}
				if scenario.removeInput {
					replace("manifest-"+digest, nil)
				}
				if scenario.readErr != nil {
					if azureTransport != nil {
						azureTransport.mu.Lock()
						azureTransport.failGet = map[string]error{key: scenario.readErr}
						azureTransport.mu.Unlock()
					} else {
						store = NewStore(markerReadFailure{Backend: backend, key: key, err: scenario.readErr})
					}
				}
				receipt, err := store.Inspect(context.Background(), digest)
				if receipt.Publication != scenario.publication || (err == nil) != scenario.success {
					t.Fatalf("publication observation changed: %+v %v", receipt, err)
				}
				if !scenario.success && (receipt.Integrity == "verified" || len(receipt.Diagnostics) == 0) {
					t.Fatalf("failed observation became verified: %+v %v", receipt, err)
				}
				completed, err := store.ReadCompleted(context.Background(), digest)
				if scenario.name == "matching" {
					if err != nil || completed == nil || !bytes.Equal(completed.ArtifactBytes[m.TerminalArtifact], terminal) {
						t.Fatalf("matching marker lost completed bytes: %+v %v", completed, err)
					}
				} else if err == nil || completed != nil {
					t.Fatalf("noncompleted observation returned source bytes: %+v %v", completed, err)
				}
			})
		}
	}
}

func (b *ambiguousCommit) Create(ctx context.Context, key string, data []byte) (bool, error) {
	created, err := b.Backend.Create(ctx, key, data)
	if err == nil && key == b.key && b.fail {
		b.fail = false
		return false, errors.New("response interrupted after commit")
	}
	return created, err
}

func TestPublicationInterruptedFinalizationRecovery(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			var backend Backend
			if provider == "local" {
				local, err := OpenLocal(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := local.Close(); err != nil {
						t.Error(err)
					}
				})
				backend = local
			} else {
				backend, _ = azureFixture(t)
			}
			m, data := publicationFixture(t)
			wire, err := Encode(m)
			if err != nil {
				t.Fatal(err)
			}
			fault := &ambiguousCommit{Backend: backend, fail: true}
			store := NewStore(fault)
			digest, err := store.Create(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Put(context.Background(), m.Artifacts[0], data); err != nil {
				t.Fatal(err)
			}
			fault.key = "final-" + digest
			receipt, err := store.Finalize(context.Background(), digest)
			if err == nil || receipt.Integrity == "verified" || receipt.Publication != "unknown" {
				t.Fatalf("interrupted delivery became success: %+v %v", receipt, err)
			}
			receipt, err = store.Inspect(context.Background(), digest)
			if err != nil || receipt.Publication != "finalized" || receipt.Integrity != "verified" {
				t.Fatalf("verified recovery lost committed publication: %+v %v", receipt, err)
			}
			receipt, err = store.Finalize(context.Background(), digest)
			if err != nil || receipt.Publication != "finalized" {
				t.Fatalf("repeat after ambiguous commit not idempotent: %+v %v", receipt, err)
			}
		})
	}
}

func TestLocalConfinedReadsRetainRootAndRejectSpecialObjects(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "root")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	data := []byte("owned bytes")
	a := Artifact{SHA256: Hash(data), SizeBytes: int64(len(data))}
	store := NewStore(backend)
	if err := store.Put(context.Background(), a, data); err != nil {
		t.Fatal(err)
	}
	relocated := filepath.Join(base, "moved")
	if err := os.Rename(directory, relocated); err != nil {
		if !retainedRootRenameBlocked(err) {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(relocated); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("blocked parent replacement created a competing destination: %v", statErr)
		}
		if _, statErr := os.Stat(directory); statErr != nil {
			t.Fatalf("blocked parent replacement lost retained root: %v", statErr)
		}
	} else {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := backend.Get(context.Background(), "blob-"+a.SHA256, a.SizeBytes)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("retained root reopened competing parent: %q %v", got, err)
	}
	key := "blob-" + strings.Repeat("b", 64)
	if err := backend.root.Mkdir(key, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Get(context.Background(), key, 10); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("directory accepted as artifact: %v", err)
	}
	for _, key := range []string{"../escape", "blob-" + strings.Repeat("g", 64), "final-attempt-" + strings.Repeat("a", 64)} {
		if _, err := backend.Get(context.Background(), key, 10); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid key admitted: %q %v", key, err)
		}
	}
	if _, err := backend.Get(context.Background(), "blob-"+a.SHA256, a.SizeBytes-1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversize content read: %v", err)
	}
}

func TestPublicationRejectsUnavailableBackendsAndInvalidAzureConfiguration(t *testing.T) {
	for _, store := range []*Store{nil, NewStore(nil)} {
		r, err := store.Inspect(context.Background(), strings.Repeat("a", 64))
		if err == nil || r.Integrity == "verified" || r.Publication != "unknown" {
			t.Fatalf("unavailable store became verified: %+v %v", r, err)
		}
		for _, digest := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
			r, err := NewStore(nil).Inspect(context.Background(), digest)
			if !errors.Is(err, ErrInvalid) || r.Publication != "unknown" || r.Integrity != "unverified" ||
				r.Artifacts == nil || r.Diagnostics == nil {
				t.Fatalf("invalid digest manufactured a publication observation: %+v %v", r, err)
			}
		}
		c, err := store.Catalog(context.Background())
		if err == nil || len(c.ManifestSHA256) != 0 {
			t.Fatalf("unavailable store produced catalog: %+v %v", c, err)
		}
	}
	for _, url := range []string{"http://offline.invalid/container", "https://user:pass@offline.invalid/container", "https://offline.invalid/container?sig=not-a-secret", "https://offline.invalid/container#fragment", "not a URL"} {
		if _, err := NewAzure(url, offlineCredential{}, nil); err == nil {
			t.Fatalf("invalid explicit Azure configuration accepted: %q", url)
		}
	}
	if _, err := NewAzure("https://offline.invalid/container", nil, nil); err == nil {
		t.Fatal("nil credential accepted")
	}
}
