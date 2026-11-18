//go:build !windows

package executioncheckpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

func TestCheckpointStorageRetainsOriginalRootAcrossReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "store")
	parked := filepath.Join(parent, "original")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := artifactmanifest.OpenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := os.Rename(root, parked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := Put(context.Background(), backend, *checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parked, "blob-"+digest)); err != nil {
		t.Fatalf("owned original root did not receive immutable checkpoint: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("checkpoint wrote into competing replacement: %v, %v", entries, err)
	}
	if value, err := Load(context.Background(), backend, digest); err != nil || value == nil {
		t.Fatalf("retained-root checkpoint read failed: %+v, %v", value, err)
	}
}

func TestCheckpointReadRejectsOutsideRootSymlink(t *testing.T) {
	backend, directory := localCheckpointStore(t)
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(*checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	digest := artifactmanifest.Hash(data)
	outside := filepath.Join(t.TempDir(), "outside-checkpoint.json")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "blob-"+digest)); err != nil {
		t.Fatal(err)
	}
	if value, err := Load(context.Background(), backend, digest); value != nil || err == nil {
		t.Fatalf("correctly hashed outside-root replacement admitted: %+v, %v", value, err)
	}
	// Neither read failure nor publication conflict removes user-owned entries.
	if _, err := Put(context.Background(), backend, *checkpoint); err == nil {
		t.Fatal("checkpoint publication accepted an outside-root competing entry")
	}
	if _, err := os.Lstat(filepath.Join(directory, "blob-"+digest)); err != nil {
		t.Fatalf("competing symlink was deleted: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside evidence was deleted: %v", err)
	}
}
