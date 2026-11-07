package executioncheckpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

func localCheckpointStore(t *testing.T) (*artifactmanifest.Local, string) {
	t.Helper()
	path := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	return backend, path
}

func TestCheckpointImmutableBlobPersistence(t *testing.T) {
	checkpoint, _, _, _, _, _ := checkpointContext(t, false)
	backend, directory := localCheckpointStore(t)
	digest, err := Put(context.Background(), backend, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := Encode(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if digest != artifactmanifest.Hash(canonical) {
		t.Fatal("published digest does not identify actual checkpoint bytes")
	}
	onDisk, err := os.ReadFile(filepath.Join(directory, "blob-"+digest))
	if err != nil || !bytes.Equal(onDisk, canonical) {
		t.Fatalf("actual immutable blob differs: %s, %v", onDisk, err)
	}
	value, err := Load(context.Background(), backend, digest)
	if err != nil || value == nil || *value != checkpoint {
		t.Fatalf("checkpoint was not restored from exact stored bytes: %+v, %v", value, err)
	}
	// The provider retains staging files. No ledger or final marker may be
	// synthesized from storing a checkpoint declaration.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "reservation") || strings.HasPrefix(entry.Name(), "completion") ||
			strings.HasPrefix(entry.Name(), "decision") || strings.HasPrefix(entry.Name(), "final-") {
			t.Fatalf("checkpoint publication invented authoritative state: %s", entry.Name())
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			repeated, err := Put(context.Background(), backend, checkpoint)
			if err != nil {
				failures <- err
			} else if repeated != digest {
				failures <- errors.New("concurrent identical checkpoint changed digest")
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	checkpoint.CreatedAt = "2026-11-07T16:00:01Z"
	other, err := Put(context.Background(), backend, checkpoint)
	if err != nil || other == digest {
		t.Fatalf("distinct checkpoint overwrote source: %s, %v", other, err)
	}
	if _, err := Load(context.Background(), backend, digest); err != nil {
		t.Fatalf("original checkpoint lost after distinct publication: %v", err)
	}
}

type checkpointFaultBackend struct {
	artifactmanifest.Backend
	createErr error
	readErr   error
	readData  []byte
	afterRead func()
	calls     int
}

func (b *checkpointFaultBackend) Create(ctx context.Context, key string, data []byte) (bool, error) {
	b.calls++
	if b.createErr != nil {
		return false, b.createErr
	}
	return b.Backend.Create(ctx, key, data)
}

func (b *checkpointFaultBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.calls++
	if b.readErr != nil {
		return nil, b.readErr
	}
	if b.readData != nil {
		return bytes.Clone(b.readData), nil
	}
	data, err := b.Backend.Get(ctx, key, limit)
	if b.afterRead != nil {
		b.afterRead()
	}
	return data, err
}

func TestCheckpointPublicationAndReadFailures(t *testing.T) {
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := localCheckpointStore(t)
	digest, err := Put(context.Background(), backend, *checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected checkpoint provider failure")
	for _, tc := range []struct {
		name string
		edit func(*checkpointFaultBackend)
	}{
		{"create", func(b *checkpointFaultBackend) { b.createErr = injected }},
		{"verification-read", func(b *checkpointFaultBackend) { b.readErr = injected }},
	} {
		fault := &checkpointFaultBackend{Backend: backend}
		tc.edit(fault)
		got, err := Put(context.Background(), fault, *checkpoint)
		if got != digest || !errors.Is(err, injected) || !strings.Contains(err.Error(), digest) {
			t.Fatalf("%s failure/residue hidden: %s, %v", tc.name, got, err)
		}
	}
	fault := &checkpointFaultBackend{Backend: backend, readData: []byte("wrong content")}
	if _, err := Put(context.Background(), fault, *checkpoint); !errors.Is(err, artifactmanifest.ErrCorrupt) {
		t.Fatalf("publication accepted corrupt readback: %v", err)
	}
	if value, err := Load(context.Background(), fault, digest); value != nil || !errors.Is(err, artifactmanifest.ErrCorrupt) {
		t.Fatalf("read accepted corrupt source: %+v, %v", value, err)
	}
	fault.readData = nil
	fault.readErr = injected
	if value, err := Load(context.Background(), fault, digest); value != nil || !errors.Is(err, injected) {
		t.Fatalf("read failure hidden: %+v, %v", value, err)
	}
	if value, err := Load(context.Background(), backend, strings.Repeat("f", 64)); value != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("missing checkpoint invented: %+v, %v", value, err)
	}
	if value, err := Load(context.Background(), nil, digest); value != nil || err == nil {
		t.Fatalf("missing backend accepted: %+v, %v", value, err)
	}
	if got, err := Put(context.Background(), nil, *checkpoint); got != digest || err == nil {
		t.Fatalf("missing publication backend accepted: %s, %v", got, err)
	}
	for _, bad := range []string{"", "../escape", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), digest + "\n"} {
		fault := &checkpointFaultBackend{Backend: backend}
		if value, err := Load(context.Background(), fault, bad); value != nil || !errors.Is(err, ErrInvalid) || fault.calls != 0 {
			t.Fatalf("invalid digest reached provider or succeeded: %q, %+v, %v, %d", bad, value, err, fault.calls)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	fault = &checkpointFaultBackend{Backend: backend}
	if got, err := Put(canceled, fault, *checkpoint); got != "" || !errors.Is(err, context.Canceled) || fault.calls != 0 {
		t.Fatalf("canceled publication reached provider: %s, %v, %d", got, err, fault.calls)
	}
	if value, err := Load(canceled, fault, digest); value != nil || !errors.Is(err, context.Canceled) || fault.calls != 0 {
		t.Fatalf("canceled read reached provider: %+v, %v, %d", value, err, fault.calls)
	}
	late, stop := context.WithCancel(context.Background())
	fault = &checkpointFaultBackend{Backend: backend, afterRead: stop}
	if got, err := Put(late, fault, *checkpoint); got != digest || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after committed bytes became success: %s, %v", got, err)
	}
	if _, err := Load(context.Background(), backend, digest); err != nil {
		t.Fatalf("late cancellation removed immutable evidence: %v", err)
	}
	late, stop = context.WithCancel(context.Background())
	fault = &checkpointFaultBackend{Backend: backend, afterRead: stop}
	if value, err := Load(late, fault, digest); value != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late read cancellation became successful restore input: %+v, %v", value, err)
	}
	fault = &checkpointFaultBackend{Backend: backend}
	checkpoint.Boundary = "mid-runtime"
	if got, err := Put(context.Background(), fault, *checkpoint); got != "" || !errors.Is(err, ErrInvalid) || fault.calls != 0 {
		t.Fatalf("invalid checkpoint published: %s, %v, %d", got, err, fault.calls)
	}
}

func TestCheckpointReadRejectsNoncanonicalAndUnrelatedBlobs(t *testing.T) {
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := Encode(*checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	backend, _ := localCheckpointStore(t)
	for _, data := range [][]byte{append(bytes.Clone(canonical), '\n'), []byte(`{"kind":"other"}`), []byte("{}")} {
		digest := artifactmanifest.Hash(data)
		descriptor := artifactmanifest.Artifact{Name: "control.json", SHA256: digest, SizeBytes: int64(len(data)), MediaType: "application/json"}
		if err := artifactmanifest.NewStore(backend).Put(context.Background(), descriptor, data); err != nil {
			t.Fatal(err)
		}
		if value, err := Load(context.Background(), backend, digest); value != nil || !errors.Is(err, artifactmanifest.ErrCorrupt) {
			t.Fatalf("correctly hashed noncheckpoint/noncanonical blob accepted: %+v, %v", value, err)
		}
	}
	fault := &checkpointFaultBackend{Backend: backend, readData: bytes.Repeat([]byte(" "), MaxBytes+1)}
	if value, err := Load(context.Background(), fault, artifactmanifest.Hash(fault.readData)); value != nil || !errors.Is(err, artifactmanifest.ErrCorrupt) {
		t.Fatalf("provider exceeding requested bound accepted: %+v, %v", value, err)
	}
}
