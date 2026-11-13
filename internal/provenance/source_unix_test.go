//go:build unix

package provenance

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDirectoryReplacedByFIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "objects"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	src, closeSrc, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeSrc() }()
	ds, ok := src.(*dirSource)
	if !ok {
		t.Fatalf("unexpected source type %T", src)
	}
	done := make(chan error, 1)
	// Simulates a queued directory swapped for a FIFO before it is opened.
	go func() { _, err := ds.readDir("objects", MaxMembers); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as a bundle directory")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listing blocked on a FIFO")
	}
}
