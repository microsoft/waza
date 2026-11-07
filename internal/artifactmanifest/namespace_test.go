package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

func namespaceFixture(t *testing.T, provider string) (Backend, *Namespace, *fakeAzure) {
	t.Helper()
	var backend Backend
	var transport *fakeAzure
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
		backend, transport = azureFixture(t)
	}
	namespace, err := NewNamespace(backend, ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	return backend, namespace, transport
}

func TestNamespaceRawBytesAndStrictArtifactBoundary(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			backend, namespace, _ := namespaceFixture(t, provider)
			key := "reservation/exec-" + strings.Repeat("a", 64)
			physical, err := namespaceKey(key)
			if err != nil {
				t.Fatal(err)
			}
			if len(physical) > 128 || strings.ContainsAny(physical, `/\`) {
				t.Fatalf("unsafe physical mapping: %q", physical)
			}
			data := []byte("{not a sidecar}\x00\xff")
			if created, err := backend.Create(context.Background(), physical, data); created || !errors.Is(err, ErrInvalid) {
				t.Fatalf("artifact namespace accepted opaque key: %v %v", created, err)
			}
			if _, err := backend.Get(context.Background(), physical, int64(len(data))); !errors.Is(err, ErrInvalid) {
				t.Fatalf("artifact Get accepted opaque key: %v", err)
			}
			created, err := namespace.Create(context.Background(), key, data)
			if !created || err != nil {
				t.Fatalf("raw namespace rejected arbitrary bytes: %v %v", created, err)
			}
			data[0] = 'X'
			got, err := namespace.ReadUnverified(context.Background(), key)
			if err != nil || string(got) != "{not a sidecar}\x00\xff" {
				t.Fatalf("stored bytes retained caller buffer or were repaired: %q %v", got, err)
			}
			got[0] = 'Y'
			got, err = namespace.ReadUnverified(context.Background(), key)
			if err != nil || string(got) != "{not a sidecar}\x00\xff" {
				t.Fatalf("read bytes were not detached: %q %v", got, err)
			}
			for _, existing := range [][]byte{got, []byte("different")} {
				created, err = namespace.Create(context.Background(), key, existing)
				if created || !errors.Is(err, ErrOccupied) {
					t.Fatalf("occupied record granted creation: %v %v", created, err)
				}
			}
			keys, truncated, err := namespace.ListUnverified(context.Background())
			if err != nil || truncated || !slices.Equal(keys, []string{key}) {
				t.Fatalf("namespace discovery lost raw pointer: %v %v %v", keys, truncated, err)
			}
			catalog, err := NewStore(backend).Catalog(context.Background())
			if err != nil || len(catalog.ManifestSHA256) != 0 {
				t.Fatalf("ledger record leaked into manifest discovery: %+v %v", catalog, err)
			}
			digestKey := "blob-" + Hash([]byte("correct"))
			if created, err := backend.Create(context.Background(), digestKey, []byte("incorrect")); created || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("raw extraction weakened artifact digest enforcement: %v %v", created, err)
			}
		})
	}
}

func TestNamespaceKeyBoundsMissingAndCancellation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			_, namespace, _ := namespaceFixture(t, provider)
			for _, key := range []string{"", "../escape", "reservation", "reservation/../escape",
				"reservation/exec-" + strings.Repeat("A", 64), "reservation/exec-" + strings.Repeat("a", 63),
				"completion/decision-" + strings.Repeat("a", 64), "decision/exec-" + strings.Repeat("a", 64),
				"other/exec-" + strings.Repeat("a", 64), "reservation/exec-" + strings.Repeat("a", 64) + "/nested"} {
				if created, err := namespace.Create(context.Background(), key, nil); created || !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid key admitted: %q %v %v", key, created, err)
				}
				if _, err := namespace.ReadUnverified(context.Background(), key); !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid read key admitted: %q %v", key, err)
				}
			}
			key := "decision/decision-" + strings.Repeat("b", 64)
			if _, err := namespace.ReadUnverified(context.Background(), key); !errors.Is(err, ErrMissing) {
				t.Fatalf("missing was not distinguished: %v", err)
			}
			if created, err := namespace.Create(context.Background(), key, bytes.Repeat([]byte("x"), MaxNamespaceObjectBytes+1)); created || !errors.Is(err, ErrInvalid) {
				t.Fatalf("oversize namespace record admitted: %v %v", created, err)
			}
			data := bytes.Repeat([]byte("x"), MaxNamespaceObjectBytes)
			if created, err := namespace.Create(context.Background(), key, data); !created || err != nil {
				t.Fatalf("exact object limit rejected: %v %v", created, err)
			}
			if got, err := namespace.ReadUnverified(context.Background(), key); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("exact read limit rejected: length=%d %v", len(got), err)
			}
			zeroKey := "completion/exec-" + strings.Repeat("c", 64)
			if created, err := namespace.Create(context.Background(), zeroKey, nil); !created || err != nil {
				t.Fatalf("raw zero bytes repaired/rejected: %v %v", created, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if created, err := namespace.Create(ctx, zeroKey, nil); created || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled creation became occupied/success: %v %v", created, err)
			}
			if _, err := namespace.ReadUnverified(ctx, key); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read lost cause: %v", err)
			}
			if _, _, err := namespace.ListUnverified(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled discovery lost cause: %v", err)
			}
		})
	}
}

func TestNamespaceExclusiveRace(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			_, namespace, _ := namespaceFixture(t, provider)
			key := "reservation/exec-" + strings.Repeat("d", 64)
			start := make(chan struct{})
			type result struct {
				created bool
				err     error
				data    []byte
			}
			results := make(chan result, 16)
			var ready sync.WaitGroup
			ready.Add(cap(results))
			for i := range cap(results) {
				go func() {
					data := fmt.Appendf(nil, "contender-%d", i)
					ready.Done()
					<-start
					created, err := namespace.Create(context.Background(), key, data)
					results <- result{created, err, data}
				}()
			}
			ready.Wait()
			close(start)
			winners := 0
			var winner []byte
			for range cap(results) {
				result := <-results
				if result.created && result.err == nil {
					winners++
					winner = result.data
				} else if result.created || !errors.Is(result.err, ErrOccupied) {
					t.Errorf("unexpected contender result: %+v", result)
				}
			}
			if winners != 1 {
				t.Fatalf("exclusive creation had %d successful creators", winners)
			}
			got, err := namespace.ReadUnverified(context.Background(), key)
			if err != nil || !bytes.Equal(got, winner) {
				t.Fatalf("winner's exact bytes were not published: %q %v", got, err)
			}
		})
	}
}

func TestNamespaceReadRejectsOversizedStoredRawObject(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			backend, namespace, transport := namespaceFixture(t, provider)
			key := "reservation/exec-" + strings.Repeat("9", 64)
			physical, err := namespaceKey(key)
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte("x"), MaxNamespaceObjectBytes+1)
			if local, ok := backend.(*Local); ok {
				if err := local.root.WriteFile(physical, data, 0o400); err != nil {
					t.Fatal(err)
				}
			} else {
				transport.objects[physical] = data
			}
			if got, err := namespace.ReadUnverified(context.Background(), key); got != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("oversized stored record was admitted: length=%d %v", len(got), err)
			}
		})
	}
}

func TestNamespaceDiscoveryTruncationAndCorruptEntries(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			backend, namespace, transport := namespaceFixture(t, provider)
			insert := func(key string) {
				t.Helper()
				if local, ok := backend.(*Local); ok {
					if err := local.root.WriteFile(key, []byte("unverified"), 0o400); err != nil {
						t.Fatal(err)
					}
				} else {
					transport.objects[key] = []byte("unverified")
				}
			}
			for i := range MaxCatalogEntries + 1 {
				key, err := namespaceKey(fmt.Sprintf("reservation/exec-%064x", i))
				if err != nil {
					t.Fatal(err)
				}
				insert(key)
			}
			keys, truncated, err := namespace.ListUnverified(context.Background())
			if err != nil || !truncated || len(keys) != MaxCatalogEntries || !slices.IsSorted(keys) {
				t.Fatalf("unbounded/incomplete discovery became complete: count=%d truncated=%v err=%v", len(keys), truncated, err)
			}
		})
		t.Run(provider+"/corrupt-entry", func(t *testing.T) {
			backend, namespace, transport := namespaceFixture(t, provider)
			physical := namespacePrefix + "bad"
			if local, ok := backend.(*Local); ok {
				if err := local.root.WriteFile(physical, nil, 0o400); err != nil {
					t.Fatal(err)
				}
			} else {
				transport.objects[physical] = nil
			}
			if keys, _, err := namespace.ListUnverified(context.Background()); keys != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corrupt discovery pointer silently admitted/ignored: %v %v", keys, err)
			}
		})
	}
}

func TestNamespaceUnsupportedAndLocalIO(t *testing.T) {
	for _, backend := range []Backend{nil, (*Local)(nil), &Local{}, (*Azure)(nil), &Azure{}, &ambiguousCommit{}} {
		if namespace, err := NewNamespace(backend, ExecutionLedgerNamespaceV1); namespace != nil || !errors.Is(err, ErrUnsupportedBackend) {
			t.Fatalf("unsupported backend was adapted/defaulted: %T %v %v", backend, namespace, err)
		}
	}
	if _, err := NewNamespace(nil, "another-namespace"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("arbitrary namespace generalized: %v", err)
	}
	local, err := OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := NewNamespace(local, ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	key := "reservation/exec-" + strings.Repeat("e", 64)
	physical, err := namespaceKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.root.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := namespace.ReadUnverified(context.Background(), key); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("directory treated as raw record: %v", err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := namespace.ReadUnverified(context.Background(), key); err == nil || errors.Is(err, ErrMissing) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("closed provider became missing/content: %v", err)
	}
	var empty *Namespace
	if _, err := empty.ReadUnverified(context.Background(), key); !errors.Is(err, ErrUnsupportedBackend) {
		t.Fatalf("nil namespace lost unavailable classification: %v", err)
	}
}

func TestNamespaceCloseFailureDoesNotGrantCreation(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			data := []byte("opaque bytes")
			injected := errors.New("injected close failure")
			var backend Backend
			var transport *fakeAzure
			if provider == "local" {
				local, _, _ := namespaceFixture(t, provider)
				raw, ok := local.(*Local)
				if !ok {
					t.Fatalf("local fixture supplied %T", local)
				}
				raw.openStage = func(name string) (stageFile, error) {
					file, err := raw.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
					if err != nil {
						return nil, err
					}
					return failingWriter{File: file, closeErr: injected}, nil
				}
				backend = raw
			} else {
				transport = &fakeAzure{objects: make(map[string][]byte)}
				backend = azureBackend(t, responseFault{inner: transport, alter: func(response *http.Response) {
					response.Body = &faultBody{Reader: bytes.NewReader(data), closeErr: injected}
				}})
			}
			namespace, err := NewNamespace(backend, ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			key := "reservation/exec-" + strings.Repeat("f", 64)
			created, err := namespace.Create(context.Background(), key, data)
			if created || !errors.Is(err, injected) {
				t.Fatalf("failed close granted creation: %v %v", created, err)
			}
			if provider != "local" {
				physical, err := namespaceKey(key)
				if err != nil || !bytes.Equal(transport.objects[physical], data) {
					t.Fatalf("failed response/readback lost committed residue: %v", err)
				}
				if created, err := namespace.Create(context.Background(), key, data); created || !errors.Is(err, ErrOccupied) {
					t.Fatalf("retry hydrated creation from failed readback: %v %v", created, err)
				}
			}
		})
	}
}

func TestNamespaceAzureReadbackAndAmbiguousCommitCannotGrantCreation(t *testing.T) {
	for _, mode := range []string{"readback", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("reservation bytes")
			transport := &fakeAzure{objects: make(map[string][]byte)}
			injected := errors.New("response interrupted")
			var backend *Azure
			if mode == "ambiguous" {
				transport.failCommittedPut = injected
				backend = azureBackend(t, transport)
			} else {
				backend = azureBackend(t, responseFault{inner: transport, alter: func(response *http.Response) {
					response.Body = &faultBody{Reader: bytes.NewReader(data), readErr: injected}
				}})
			}
			namespace, err := NewNamespace(backend, ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			key := "reservation/exec-" + strings.Repeat("1", 64)
			created, err := namespace.Create(context.Background(), key, data)
			if created || !errors.Is(err, injected) {
				t.Fatalf("ambiguous failure granted creation: %v %v", created, err)
			}
			physical, err := namespaceKey(key)
			if err != nil || !bytes.Equal(transport.objects[physical], data) {
				t.Fatalf("committed reservation residue lost: %v", err)
			}
			if created, err := namespace.Create(context.Background(), key, data); created || !errors.Is(err, ErrOccupied) {
				t.Fatalf("occupied retry granted creation after ambiguity: %v %v", created, err)
			}
		})
	}
}
