package artifactmanifest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

var errNamespaceReadBudget = errors.New("injected shared read budget exhausted")

type namespaceReadBudget struct {
	remaining int64
	reads     int
}

type budgetedNamespaceRead struct {
	ReadBackend
	budget *namespaceReadBudget
}

func (r budgetedNamespaceRead) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if limit < 0 || limit >= r.budget.remaining {
		return nil, errNamespaceReadBudget
	}
	r.budget.remaining -= limit + 1
	r.budget.reads++
	data, err := r.ReadBackend.Get(ctx, key, limit)
	if err == nil {
		r.budget.remaining += limit + 1 - int64(len(data))
	}
	return data, err
}

type budgetedManifestBackend struct {
	Backend
	reader ReadBackend
}

type namespaceReadFunc func(context.Context, string, int64) ([]byte, error)

func (f namespaceReadFunc) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return f(ctx, key, limit)
}

func (b budgetedManifestBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.reader.Get(ctx, key, limit)
}

func TestNamespaceReaderSharesPreReadBudgetWithCompletedStore(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline"} {
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
			namespace, err := NewNamespace(backend, ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			key := "reservation/exec-" + strings.Repeat("a", 64)
			original := []byte("opaque record; not canonical or verified")
			if created, err := namespace.Create(context.Background(), key, original); err != nil || !created {
				t.Fatalf("actual namespace setup: %v %v", created, err)
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
			if _, err := store.Finalize(context.Background(), digest); err != nil {
				t.Fatal(err)
			}
			budget := &namespaceReadBudget{remaining: MaxNamespaceObjectBytes + 1}
			var source ReadBackend
			reader, err := namespace.Reader(func(raw ReadBackend) ReadBackend {
				source = raw
				return budgetedNamespaceRead{ReadBackend: raw, budget: budget}
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, writable := source.(Backend); writable {
				t.Fatal("decorator received a writable provider")
			}
			data, err := reader.ReadUnverified(context.Background(), key)
			if err != nil || !slices.Equal(data, original) || budget.remaining != MaxNamespaceObjectBytes+1-int64(len(original)) {
				t.Fatalf("raw read/refund: %q remaining=%d %v", data, budget.remaining, err)
			}
			data[0] = 'x'
			again, err := source.Get(context.Background(), key, MaxNamespaceObjectBytes)
			if err != nil || !slices.Equal(again, original) {
				t.Fatalf("raw returned bytes alias stored bytes: %q %v", again, err)
			}
			for _, invalid := range []string{"manifest-" + digest, "../escape", "checkpoint/exec-" + strings.Repeat("a", 64)} {
				if _, err := source.Get(context.Background(), invalid, 1); !errors.Is(err, ErrInvalid) {
					t.Fatalf("read capability escaped namespace: %q %v", invalid, err)
				}
			}
			for _, limit := range []int64{-1, MaxNamespaceObjectBytes + 1} {
				if _, err := source.Get(context.Background(), key, limit); !errors.Is(err, ErrInvalid) {
					t.Fatalf("read capability admitted limit %d: %v", limit, err)
				}
			}
			if data, err := source.Get(context.Background(), key, int64(len(original))); err != nil || !slices.Equal(data, original) {
				t.Fatalf("exact raw byte bound: %q %v", data, err)
			}
			if data, err := source.Get(context.Background(), key, int64(len(original)-1)); !errors.Is(err, ErrCorrupt) || data != nil {
				t.Fatalf("over-bound raw bytes admitted: len=%d %v", len(data), err)
			}
			emptyKey := "decision/decision-" + strings.Repeat("d", 64)
			if created, err := namespace.Create(context.Background(), emptyKey, []byte{}); err != nil || !created {
				t.Fatalf("explicit empty object setup: %v %v", created, err)
			}
			if data, err := source.Get(context.Background(), emptyKey, 0); err != nil || len(data) != 0 {
				t.Fatalf("present empty raw object: %q %v", data, err)
			}
			budget.remaining = MaxNamespaceObjectBytes
			beforeLimit := budget.reads
			if _, err := reader.ReadUnverified(context.Background(), key); !errors.Is(err, errNamespaceReadBudget) || budget.reads != beforeLimit {
				t.Fatalf("one-byte insufficient reservation performed I/O: %v", err)
			}
			budget.remaining = MaxNamespaceObjectBytes + 1
			missing := "completion/exec-" + strings.Repeat("b", 64)
			if _, err := reader.ReadUnverified(context.Background(), missing); !errors.Is(err, ErrMissing) || budget.remaining != 0 {
				t.Fatalf("failed read did not consume full reservation: remaining=%d %v", budget.remaining, err)
			}
			before := budget.reads
			if _, err := reader.ReadUnverified(context.Background(), key); !errors.Is(err, errNamespaceReadBudget) {
				t.Fatalf("namespace read bypassed exhausted shared cap: %v", err)
			}
			boundedStore := NewStore(budgetedManifestBackend{Backend: backend,
				reader: budgetedNamespaceRead{ReadBackend: backend, budget: budget}})
			if completed, err := boundedStore.ReadCompleted(context.Background(), digest); !errors.Is(err, errNamespaceReadBudget) || completed != nil {
				t.Fatalf("completed-store read bypassed shared cap: %+v %v", completed, err)
			}
			if budget.reads != before {
				t.Fatal("exhausted namespace/manifest budget performed provider reads")
			}
			budget.remaining = MaxTotalBytes
			if _, err := reader.ReadUnverified(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			completed, err := boundedStore.ReadCompleted(context.Background(), digest)
			if err != nil || completed == nil || !slices.Equal(completed.ArtifactBytes[m.TerminalArtifact], terminal) {
				t.Fatalf("positive shared-budget completed source: %+v %v", completed, err)
			}
		})
	}
}

func TestNamespaceReaderRejectsInvalidConstruction(t *testing.T) {
	for _, namespace := range []*Namespace{nil, {}} {
		if reader, err := namespace.Reader(nil); !errors.Is(err, ErrUnsupportedBackend) || reader != nil {
			t.Fatalf("unadmitted reader: %+v %v", reader, err)
		}
	}
	local, err := OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := local.Close(); err != nil {
			t.Error(err)
		}
	}()
	namespace, err := NewNamespace(local, ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	if reader, err := namespace.Reader(func(ReadBackend) ReadBackend { return nil }); !errors.Is(err, ErrInvalid) || reader != nil {
		t.Fatalf("nil decorator output admitted: %+v %v", reader, err)
	}
	if reader, err := namespace.Reader(nil); !errors.Is(err, ErrInvalid) || reader != nil {
		t.Fatalf("nil decorator admitted: %+v %v", reader, err)
	}
	reader, err := namespace.Reader(func(raw ReadBackend) ReadBackend { return raw })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadUnverified(context.Background(), "invalid"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := (*NamespaceReader)(nil).ReadUnverified(context.Background(), "reservation/exec-"+strings.Repeat("a", 64)); !errors.Is(err, ErrUnsupportedBackend) {
		t.Fatal(err)
	}
	key := "reservation/exec-" + strings.Repeat("a", 64)
	injected := errors.New("injected decorator failure")
	for _, scenario := range []struct {
		data []byte
		err  error
		want error
	}{
		{[]byte("partial"), injected, injected},
		{make([]byte, MaxNamespaceObjectBytes+1), nil, ErrCorrupt},
	} {
		reader, err := namespace.Reader(func(ReadBackend) ReadBackend {
			return namespaceReadFunc(func(context.Context, string, int64) ([]byte, error) {
				return scenario.data, scenario.err
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if data, err := reader.ReadUnverified(context.Background(), key); !errors.Is(err, scenario.want) || data != nil {
			t.Fatalf("failed/oversized decorator bytes admitted: len=%d %v", len(data), err)
		}
	}
	shared := []byte("caller-owned raw bytes")
	reader, err = namespace.Reader(func(ReadBackend) ReadBackend {
		return namespaceReadFunc(func(context.Context, string, int64) ([]byte, error) { return shared, nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := reader.ReadUnverified(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'x'
	if string(shared) != "caller-owned raw bytes" {
		t.Fatal("returned bytes alias decorator state")
	}
}
