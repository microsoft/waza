// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"
)

// mockGitHub returns a resolver wired to two httptest servers that emulate the
// GitHub API and raw content endpoints. The returned map of file bodies is
// consulted by the raw server; keys are "<owner>/<repo>/<commit>/<path>".
func mockGitHub(t *testing.T, commit string, files map[string]string) (*Resolver, func()) {
	t.Helper()

	apiHits := map[string]int{}
	rawHits := map[string]int{}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHits[r.URL.Path]++
		// Only the commits API is needed.
		if !strings.HasPrefix(r.URL.Path, "/repos/") {
			http.Error(w, "not implemented", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"sha":%q}`, commit)
	}))

	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		rawHits[key]++
		body, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, body)
	}))

	fetcher := &GitHubFetcher{
		HTTP:    http.DefaultClient,
		BaseAPI: api.URL,
		BaseRaw: raw.URL,
	}
	res := &Resolver{
		Fetcher:  fetcher,
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return time.Unix(0, 0).UTC() },
	}
	teardown := func() {
		api.Close()
		raw.Close()
	}
	return res, teardown
}

func TestResolve_TagFetchDigestAndCache(t *testing.T) {
	commit := strings.Repeat("a", 40)
	preset := "type: code\nname: factuality\nconfig:\n  x: 1\n"
	manifest := "schema_version: 1\nmodule: github.com/waza-evals/fact\nexports:\n  graders:\n    factuality:\n      path: graders/factuality.yaml\n"

	files := map[string]string{
		"waza-evals/fact/" + commit + "/waza.registry.yaml":      manifest,
		"waza-evals/fact/" + commit + "/graders/factuality.yaml": preset,
	}
	res, teardown := mockGitHub(t, commit, files)
	defer teardown()

	ref, err := ParseRef("github.com/waza-evals/fact#factuality@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	lock := &Lockfile{SchemaVersion: LockfileSchemaVersion}
	got, err := res.Resolve(context.Background(), ref, lock, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got.Lock.Commit != commit {
		t.Fatalf("commit = %q, want %q", got.Lock.Commit, commit)
	}
	sum := sha256.Sum256([]byte(preset))
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	if got.Lock.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", got.Lock.Digest, wantDigest)
	}
	if string(got.PresetYAML) != preset {
		t.Fatalf("PresetYAML mismatch")
	}

	// Second resolve uses the lock — cache hit path, no digest error.
	got2, err := res.Resolve(context.Background(), ref, lock, false)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got2.Lock.Digest != wantDigest {
		t.Fatalf("cached digest mismatch")
	}
}

func TestResolve_RefNotInLock(t *testing.T) {
	res := &Resolver{
		Fetcher:  &stubFetcher{},
		CacheDir: t.TempDir(),
		Now:      time.Now,
	}
	ref, _ := ParseRef("github.com/o/r#x@v1.0.0")
	lock := &Lockfile{SchemaVersion: LockfileSchemaVersion}
	_, err := res.Resolve(context.Background(), ref, lock, false)
	if !errors.Is(err, ErrRefNotInLock) {
		t.Fatalf("expected ErrRefNotInLock, got %v", err)
	}
}

func TestResolve_DigestMismatch(t *testing.T) {
	commit := strings.Repeat("b", 40)
	presetGood := "type: code\nname: g\n"
	manifest := "schema_version: 1\nmodule: github.com/o/r\nexports:\n  graders:\n    g:\n      path: g.yaml\n"
	files := map[string]string{
		"o/r/" + commit + "/waza.registry.yaml": manifest,
		"o/r/" + commit + "/g.yaml":             presetGood,
	}
	res, teardown := mockGitHub(t, commit, files)
	defer teardown()

	ref, _ := ParseRef("github.com/o/r#g@" + commit)
	// Pre-seed lock with a bad digest for the same commit.
	lock := &Lockfile{
		SchemaVersion: LockfileSchemaVersion,
		Modules: []LockModule{{
			Ref: ref.Raw, Module: ref.Module(), Version: ref.Version,
			Commit: commit, Digest: "sha256:0000",
		}},
	}
	_, err := res.Resolve(context.Background(), ref, lock, false)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
}

func TestResolve_SubpathNoManifest(t *testing.T) {
	commit := strings.Repeat("c", 40)
	preset := "type: text\nname: sub\n"
	files := map[string]string{
		"o/r/" + commit + "/graders/sub.yaml": preset,
	}
	res, teardown := mockGitHub(t, commit, files)
	defer teardown()

	// Subpath form — no manifest lookup should be attempted.
	ref, err := ParseRef("github.com/o/r/graders/sub@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	lock := &Lockfile{SchemaVersion: LockfileSchemaVersion}
	got, err := res.Resolve(context.Background(), ref, lock, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got.PresetYAML) != preset {
		t.Fatalf("PresetYAML mismatch: %q", got.PresetYAML)
	}
	if got.Lock.Commit != commit {
		t.Fatalf("commit mismatch")
	}
}

func TestDefaultCacheDir(t *testing.T) {
	got, err := DefaultCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, path.Join(".waza", "cache")) {
		t.Fatalf("DefaultCacheDir() = %q, want to contain .waza/cache", got)
	}
}

type stubFetcher struct{}

func (stubFetcher) ResolveCommit(_ context.Context, _ Ref) (string, error) {
	return "", errors.New("not implemented")
}
func (stubFetcher) FetchFile(_ context.Context, _ Ref, _, _ string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
