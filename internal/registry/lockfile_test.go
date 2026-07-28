// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockfile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "waza.lock")

	lock := &Lockfile{
		SchemaVersion: LockfileSchemaVersion,
		Modules: []LockModule{
			{
				Ref: "github.com/waza-evals/fact#factuality@v1.0.0", Module: "github.com/waza-evals/fact",
				Version: "v1.0.0", Commit: "abcdef0123456789abcdef0123456789abcdef01",
				Digest: "sha256:deadbeef", URL: "https://example/x", ResolvedAt: time.Unix(0, 0).UTC(),
			},
			{
				Ref: "github.com/o/r/sub@v0.1.0", Module: "github.com/o/r",
				Version: "v0.1.0", Commit: "1111111111111111111111111111111111111111",
				Digest: "sha256:cafebabe",
			},
		},
	}
	if err := lock.Save(path); err != nil {
		t.Fatal(err)
	}

	// Header comment should be present.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsBytes(raw, []byte("waza.lock")) {
		t.Fatalf("expected header comment in lockfile:\n%s", raw)
	}

	got, err := LoadLockfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(got.Modules))
	}
	// Sorted output check.
	if got.Modules[0].Ref > got.Modules[1].Ref {
		t.Fatalf("modules not sorted by ref: %+v", got.Modules)
	}
}

func TestLockfile_LookupUpsert(t *testing.T) {
	lock := &Lockfile{SchemaVersion: LockfileSchemaVersion}
	if lock.Lookup("missing") != nil {
		t.Fatal("expected nil for missing ref")
	}
	entry := LockModule{Ref: "github.com/o/r#x@v1.0.0", Commit: "aaaa", Digest: "sha256:z"}
	lock.Upsert(entry)
	got := lock.Lookup(entry.Ref)
	if got == nil || got.Commit != "aaaa" {
		t.Fatalf("Lookup after Upsert failed: %+v", got)
	}
	// Replace.
	entry.Commit = "bbbb"
	lock.Upsert(entry)
	if got := lock.Lookup(entry.Ref); got.Commit != "bbbb" {
		t.Fatalf("Upsert did not replace: %+v", got)
	}
	if len(lock.Modules) != 1 {
		t.Fatalf("expected 1 module after upsert-replace, got %d", len(lock.Modules))
	}
}

func TestLoadLockfile_Missing(t *testing.T) {
	got, err := LoadLockfile(filepath.Join(t.TempDir(), "waza.lock"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatal("expected nil lock for missing file")
	}
}

func TestLoadLockfile_BadSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "waza.lock")
	if err := os.WriteFile(path, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLockfile(path); err == nil {
		t.Fatal("expected schema_version error")
	}
}

func TestLockfilePath(t *testing.T) {
	got := LockfilePath("/tmp/evals/eval.yaml")
	want := "/tmp/evals/waza.lock"
	if got != want {
		t.Fatalf("LockfilePath = %q, want %q", got, want)
	}
}

func containsBytes(hay, needle []byte) bool {
	return len(needle) == 0 || bytesIndex(hay, needle) >= 0
}

// bytesIndex is a tiny local Index to avoid importing bytes.
func bytesIndex(s, sep []byte) int {
	n := len(sep)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		match := true
		for j := 0; j < n; j++ {
			if s[i+j] != sep[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
