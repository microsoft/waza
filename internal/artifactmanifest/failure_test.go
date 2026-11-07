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
			if err == nil || receipt.Integrity == "verified" {
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
		if err == nil || r.Integrity == "verified" {
			t.Fatalf("unavailable store became verified: %+v %v", r, err)
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
