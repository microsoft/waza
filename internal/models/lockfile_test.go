package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockfileReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockfileName)
	lock := NewLockfile()
	lock.UpsertGrader(LockfileGrader{
		Ref:    "github.com/waza-evals/fact#factuality@v1.0.0",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Digest: "sha256:abc123",
		URL:    "https://github.com/waza-evals/fact.git",
	})

	if err := WriteLockfile(path, lock); err != nil {
		t.Fatalf("WriteLockfile() error = %v", err)
	}

	loaded, err := LoadLockfile(path)
	if err != nil {
		t.Fatalf("LoadLockfile() error = %v", err)
	}
	entry, ok := loaded.Grader("github.com/waza-evals/fact#factuality@v1.0.0")
	if !ok {
		t.Fatalf("expected lock entry")
	}
	if entry.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("Commit = %q", entry.Commit)
	}
	if entry.Digest != "sha256:abc123" {
		t.Fatalf("Digest = %q", entry.Digest)
	}
	if entry.URL != "https://github.com/waza-evals/fact.git" {
		t.Fatalf("URL = %q", entry.URL)
	}
}

func TestLoadLockfileRejectsDuplicateRefs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockfileName)
	data := []byte(`schema_version: 1
graders:
  - ref: github.com/waza-evals/fact#factuality@v1.0.0
    commit: abc
    digest: sha256:one
    url: https://github.com/waza-evals/fact.git
  - ref: github.com/waza-evals/fact#factuality@v1.0.0
    commit: def
    digest: sha256:two
    url: https://github.com/waza-evals/fact.git
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	if _, err := LoadLockfile(path); err == nil {
		t.Fatalf("expected duplicate ref error")
	}
}

func TestWriteLockfileKeepsLookupIndexInSortedOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockfileName)
	lock := NewLockfile()
	lock.UpsertGrader(LockfileGrader{
		Ref:    "github.com/waza-evals/z#grader@v1.0.0",
		Commit: "z",
		Digest: "sha256:z",
		URL:    "https://github.com/waza-evals/z.git",
	})
	lock.UpsertGrader(LockfileGrader{
		Ref:    "github.com/waza-evals/a#grader@v1.0.0",
		Commit: "a",
		Digest: "sha256:a",
		URL:    "https://github.com/waza-evals/a.git",
	})

	if err := WriteLockfile(path, lock); err != nil {
		t.Fatalf("WriteLockfile() error = %v", err)
	}
	entry, ok := lock.Grader("github.com/waza-evals/a#grader@v1.0.0")
	if !ok {
		t.Fatalf("expected sorted lock entry lookup")
	}
	if entry.Commit != "a" {
		t.Fatalf("Commit = %q, want a", entry.Commit)
	}
}
