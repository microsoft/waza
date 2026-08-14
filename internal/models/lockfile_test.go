package models

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testLockCommitA = "0123456789abcdef0123456789abcdef01234567"
	testLockCommitB = "abcdef0123456789abcdef0123456789abcdef01"
	testLockDigestA = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testLockDigestB = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func TestLockfileReadWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockfileName)
	lock := NewLockfile()
	lock.UpsertGrader(LockfileGrader{
		Ref:    "github.com/waza-evals/fact#factuality@v1.0.0",
		Commit: testLockCommitA,
		Digest: testLockDigestA,
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
	if entry.Commit != testLockCommitA {
		t.Fatalf("Commit = %q", entry.Commit)
	}
	if entry.Digest != testLockDigestA {
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
    commit: 0123456789abcdef0123456789abcdef01234567
    digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    url: https://github.com/waza-evals/fact.git
  - ref: github.com/waza-evals/fact#factuality@v1.0.0
    commit: abcdef0123456789abcdef0123456789abcdef01
    digest: sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
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
		Commit: testLockCommitB,
		Digest: testLockDigestB,
		URL:    "https://github.com/waza-evals/z.git",
	})
	lock.UpsertGrader(LockfileGrader{
		Ref:    "github.com/waza-evals/a#grader@v1.0.0",
		Commit: testLockCommitA,
		Digest: testLockDigestA,
		URL:    "https://github.com/waza-evals/a.git",
	})

	if err := WriteLockfile(path, lock); err != nil {
		t.Fatalf("WriteLockfile() error = %v", err)
	}
	entry, ok := lock.Grader("github.com/waza-evals/a#grader@v1.0.0")
	if !ok {
		t.Fatalf("expected sorted lock entry lookup")
	}
	if entry.Commit != testLockCommitA {
		t.Fatalf("Commit = %q, want %s", entry.Commit, testLockCommitA)
	}
}

func TestLoadLockfileRejectsInvalidCommitAndDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockfileName)
	data := []byte(`schema_version: 1
graders:
  - ref: github.com/waza-evals/fact#factuality@v1.0.0
    commit: ../outside
    digest: sha256:abc
    url: https://github.com/waza-evals/fact.git
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	if _, err := LoadLockfile(path); err == nil {
		t.Fatalf("expected invalid commit error")
	}
}
