// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrRefNotInLock is returned when a ref is required by eval.yaml but has no
// entry in waza.lock and the caller is running in strict (verify-only) mode.
var ErrRefNotInLock = errors.New("ref not in waza.lock; run `waza get` to resolve")

// ErrDigestMismatch is returned when cached or freshly-fetched content does
// not match the digest recorded in the lockfile.
var ErrDigestMismatch = errors.New("content digest does not match lockfile")

// ResolvedGrader is the output of resolving one grader-preset ref: the raw
// YAML bytes of the remote preset plus the lock entry that pinned it.
type ResolvedGrader struct {
	Ref        Ref
	Lock       LockModule
	PresetYAML []byte
}

// Fetcher abstracts the source backend (GitHub over HTTP by default) so tests
// can inject a mock. All methods take a context so callers can enforce timeouts.
type Fetcher interface {
	// ResolveCommit turns a version selector (semver tag OR commit SHA) into
	// a concrete 40-char commit SHA. Implementations should short-circuit
	// when the version is already a SHA.
	ResolveCommit(ctx context.Context, ref Ref) (string, error)
	// FetchFile downloads a single file from the module at the given commit
	// SHA. Path is relative to the repo root.
	FetchFile(ctx context.Context, ref Ref, commit, path string) ([]byte, error)
}

// Resolver expands refs into concrete grader-preset YAML using a Fetcher,
// content-addressed disk cache, and lockfile for reproducibility.
type Resolver struct {
	Fetcher  Fetcher
	CacheDir string
	// Now is injected for deterministic tests.
	Now func() time.Time
}

// NewResolver returns a Resolver with the default HTTP-backed GitHub fetcher
// and cache location.
func NewResolver() (*Resolver, error) {
	cache, err := DefaultCacheDir()
	if err != nil {
		return nil, err
	}
	return &Resolver{
		Fetcher:  &GitHubFetcher{HTTP: http.DefaultClient},
		CacheDir: cache,
		Now:      time.Now,
	}, nil
}

// DefaultCacheDir returns the module cache root: ~/.waza/cache.
// (The design doc suggests $XDG_CACHE_HOME; ~/.waza/cache is chosen for Phase 1
// per issue #15's spec so users have one predictable location.)
func DefaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir for module cache: %w", err)
	}
	return filepath.Join(home, ".waza", "cache"), nil
}

// cachePath returns the on-disk location for a module at the given commit.
func (r *Resolver) cachePath(ref Ref, commit string) string {
	return filepath.Join(r.CacheDir, ref.Host, ref.Owner, ref.Repo, commit)
}

// Resolve returns the resolved grader for the given ref, using the lockfile
// to enforce reproducibility.
//
// Behavior:
//   - If lock has an entry, use its pinned commit + digest. Cache miss triggers
//     a download; digest mismatch returns ErrDigestMismatch.
//   - If lock has no entry and updateLock is false, returns ErrRefNotInLock.
//   - If updateLock is true, resolves the version to a commit, fetches, computes
//     the digest, and writes/updates the lockfile in memory (caller persists).
func (r *Resolver) Resolve(ctx context.Context, ref Ref, lock *Lockfile, updateLock bool) (*ResolvedGrader, error) {
	// Fast path: locked entry.
	if entry := lock.Lookup(ref.Raw); entry != nil {
		return r.resolveLocked(ctx, ref, *entry)
	}

	if !updateLock {
		return nil, fmt.Errorf("%w: %s", ErrRefNotInLock, ref.Raw)
	}

	// Update path: resolve version -> commit and fetch fresh.
	commit, err := r.Fetcher.ResolveCommit(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolving commit for %s: %w", ref.Raw, err)
	}
	if !commitSHARE.MatchString(commit) {
		return nil, fmt.Errorf("resolving commit for %s: fetcher returned invalid SHA %q", ref.Raw, commit)
	}

	presetPath, err := r.fetchAndCacheModule(ctx, ref, commit)
	if err != nil {
		return nil, err
	}
	presetYAML, err := os.ReadFile(presetPath)
	if err != nil {
		return nil, fmt.Errorf("reading cached preset %s: %w", presetPath, err)
	}
	digest := computeDigest(presetYAML)
	url := githubRawURL(ref, commit, "") // module-relative URL noted below
	entry := LockModule{
		Ref:        ref.Raw,
		Module:     ref.Module(),
		Version:    ref.Version,
		Commit:     commit,
		Digest:     digest,
		URL:        url,
		ResolvedAt: r.Now(),
	}
	lock.Upsert(entry)
	return &ResolvedGrader{Ref: ref, Lock: entry, PresetYAML: presetYAML}, nil
}

// resolveLocked verifies a locked ref's cached content matches the recorded
// digest, fetching from the source if the cache is cold.
func (r *Resolver) resolveLocked(ctx context.Context, ref Ref, entry LockModule) (*ResolvedGrader, error) {
	if !commitSHARE.MatchString(entry.Commit) {
		return nil, fmt.Errorf("lock entry for %s: invalid commit SHA %q", ref.Raw, entry.Commit)
	}

	// Determine the preset path relative to the module root.
	manifest, err := r.loadCachedManifest(ctx, ref, entry.Commit)
	if err != nil {
		return nil, err
	}
	presetRel, err := manifest.ResolveGraderPath(ref)
	if err != nil {
		return nil, err
	}

	dir := r.cachePath(ref, entry.Commit)
	presetPath := filepath.Join(dir, "source", presetRel)
	presetYAML, err := os.ReadFile(presetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading cached preset %s: %w", presetPath, err)
		}
		// Cache miss for this file: refetch just the preset.
		data, ferr := r.Fetcher.FetchFile(ctx, ref, entry.Commit, presetRel)
		if ferr != nil {
			return nil, fmt.Errorf("fetching preset %s: %w", ref.Raw, ferr)
		}
		if err := writeCacheFile(presetPath, data); err != nil {
			return nil, err
		}
		presetYAML = data
	}

	got := computeDigest(presetYAML)
	if got != entry.Digest {
		return nil, fmt.Errorf("%w: ref %s: cached digest %s does not match lockfile %s", ErrDigestMismatch, ref.Raw, got, entry.Digest)
	}
	return &ResolvedGrader{Ref: ref, Lock: entry, PresetYAML: presetYAML}, nil
}

// fetchAndCacheModule downloads waza.registry.yaml (if the ref uses #export)
// and the target preset file into the cache, returning the on-disk path of
// the preset.
func (r *Resolver) fetchAndCacheModule(ctx context.Context, ref Ref, commit string) (string, error) {
	dir := r.cachePath(ref, commit)
	if err := os.MkdirAll(filepath.Join(dir, "source"), 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir %s: %w", dir, err)
	}

	// Fetch the manifest when needed to resolve export -> path.
	var manifest *Manifest
	if ref.Export != "" {
		manifestBytes, err := r.Fetcher.FetchFile(ctx, ref, commit, ManifestFileName)
		if err != nil {
			return "", fmt.Errorf("fetching %s: %w", ManifestFileName, err)
		}
		if err := writeCacheFile(filepath.Join(dir, "source", ManifestFileName), manifestBytes); err != nil {
			return "", err
		}
		manifest, err = ParseManifest(manifestBytes)
		if err != nil {
			return "", err
		}
	}

	presetRel, err := manifest.ResolveGraderPath(ref)
	if err != nil {
		return "", err
	}
	presetBytes, err := r.Fetcher.FetchFile(ctx, ref, commit, presetRel)
	if err != nil {
		return "", fmt.Errorf("fetching preset %s: %w", presetRel, err)
	}
	presetPath := filepath.Join(dir, "source", presetRel)
	if err := writeCacheFile(presetPath, presetBytes); err != nil {
		return "", err
	}
	return presetPath, nil
}

// loadCachedManifest loads the manifest from cache, falling back to fetching
// if it is missing. Returns nil manifest when the ref does not use #export.
func (r *Resolver) loadCachedManifest(ctx context.Context, ref Ref, commit string) (*Manifest, error) {
	if ref.Export == "" {
		return nil, nil
	}
	manifestPath := filepath.Join(r.cachePath(ref, commit), "source", ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading cached manifest %s: %w", manifestPath, err)
		}
		fetched, ferr := r.Fetcher.FetchFile(ctx, ref, commit, ManifestFileName)
		if ferr != nil {
			return nil, fmt.Errorf("fetching %s: %w", ManifestFileName, ferr)
		}
		if err := writeCacheFile(manifestPath, fetched); err != nil {
			return nil, err
		}
		data = fetched
	}
	return ParseManifest(data)
}

func writeCacheFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating cache dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing cache file %s: %w", path, err)
	}
	return nil
}

// computeDigest returns "sha256:<hex>" of the given content.
func computeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// GitHubFetcher implements Fetcher against github.com using the public REST
// and raw-content endpoints. Private repos work when GH_TOKEN or GITHUB_TOKEN
// is set in the environment; no other authentication is attempted.
type GitHubFetcher struct {
	HTTP *http.Client
	// TokenEnv is optional override for the env vars checked for auth.
	// Defaults to ["GH_TOKEN", "GITHUB_TOKEN"].
	TokenEnv []string
	// BaseAPI overrides the API base URL (for testing).
	BaseAPI string
	// BaseRaw overrides the raw content base URL (for testing).
	BaseRaw string
}

func (g *GitHubFetcher) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return http.DefaultClient
}

func (g *GitHubFetcher) token() string {
	envs := g.TokenEnv
	if len(envs) == 0 {
		envs = []string{"GH_TOKEN", "GITHUB_TOKEN"}
	}
	for _, e := range envs {
		if v := os.Getenv(e); v != "" {
			return v
		}
	}
	return ""
}

// ResolveCommit calls the GitHub commits API to resolve a tag or ref to a SHA.
func (g *GitHubFetcher) ResolveCommit(ctx context.Context, ref Ref) (string, error) {
	if ref.IsCommitSHA() {
		return ref.Version, nil
	}
	base := g.BaseAPI
	if base == "" {
		base = "https://api.github.com"
	}
	// Using /repos/{owner}/{repo}/commits/{ref} lets GitHub resolve tags,
	// branches, and short SHAs to a full commit.
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", base, ref.Owner, ref.Repo, ref.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := g.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("parsing commits response: %w", err)
	}
	if payload.SHA == "" {
		return "", fmt.Errorf("commits API returned empty sha for %s", ref.Raw)
	}
	return payload.SHA, nil
}

// FetchFile downloads a file from github.com's raw content endpoint.
func (g *GitHubFetcher) FetchFile(ctx context.Context, ref Ref, commit, path string) ([]byte, error) {
	url := githubRawURL(ref, commit, path)
	if g.BaseRaw != "" {
		url = fmt.Sprintf("%s/%s/%s/%s/%s", strings.TrimRight(g.BaseRaw, "/"), ref.Owner, ref.Repo, commit, path)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := g.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: not found at commit %s (path %s)", ref.Raw, commit, path)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

// githubRawURL builds the raw.githubusercontent.com URL for a file at a commit.
func githubRawURL(ref Ref, commit, path string) string {
	if path == "" {
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/", ref.Owner, ref.Repo, commit)
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", ref.Owner, ref.Repo, commit, path)
}
