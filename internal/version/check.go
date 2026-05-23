// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

// Package version provides non-blocking version checking against GitHub releases.
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	defaultOwner = "microsoft"
	defaultRepo  = "waza"
	cacheTTL     = 24 * time.Hour
	cacheFile    = "version-check.json"
	httpTimeout  = 5 * time.Second
)

const (
	// BashInstallScriptURL is the official Bash installer for macOS, Linux, and Windows Bash environments.
	BashInstallScriptURL = "https://raw.githubusercontent.com/microsoft/waza/main/install.sh"
	// PowerShellInstallScriptURL is the official PowerShell installer for native Windows environments.
	PowerShellInstallScriptURL = "https://raw.githubusercontent.com/microsoft/waza/main/install.ps1"
	// InstallScriptURL is the default Unix-like installer URL retained for existing callers.
	InstallScriptURL = BashInstallScriptURL
	// DefaultUpdateCommand is the recommended command for upgrading waza.
	DefaultUpdateCommand = "waza update"
)

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

type cacheEntry struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

// CheckResult holds the outcome of a version comparison.
type CheckResult struct {
	CurrentVersion string
	LatestVersion  string
	UpdateAvail    bool
}

// Checker performs async version checks against GitHub releases.
type Checker struct {
	currentVersion string
	owner          string
	repo           string
	cacheDir       string
	httpClient     *http.Client
	apiBaseURL     string

	mu     sync.Mutex
	result *CheckResult
	done   chan struct{}
}

// Option configures a Checker.
type Option func(*Checker)

// WithHTTPClient sets a custom HTTP client (useful for testing).
func WithHTTPClient(c *http.Client) Option {
	return func(ch *Checker) { ch.httpClient = c }
}

// WithCacheDir overrides the default cache directory.
func WithCacheDir(dir string) Option {
	return func(ch *Checker) { ch.cacheDir = dir }
}

// WithAPIBaseURL overrides the GitHub API base URL (for testing).
func WithAPIBaseURL(url string) Option {
	return func(ch *Checker) { ch.apiBaseURL = url }
}

// WithRepo overrides the owner/repo for the release check.
func WithRepo(owner, repo string) Option {
	return func(ch *Checker) {
		ch.owner = owner
		ch.repo = repo
	}
}

// NewChecker creates a version checker for the given current version.
func NewChecker(currentVersion string, opts ...Option) *Checker {
	ch := &Checker{
		currentVersion: currentVersion,
		owner:          defaultOwner,
		repo:           defaultRepo,
		httpClient:     &http.Client{Timeout: httpTimeout},
		apiBaseURL:     "https://api.github.com",
		done:           make(chan struct{}),
	}
	for _, o := range opts {
		o(ch)
	}
	if ch.cacheDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			ch.cacheDir = filepath.Join(home, ".waza")
		}
	}
	return ch
}

// Run starts the version check in a background goroutine. It is non-blocking.
func (c *Checker) Run(ctx context.Context) {
	go func() {
		defer close(c.done)
		result := c.check(ctx)
		c.mu.Lock()
		c.result = result
		c.mu.Unlock()
	}()
}

// Result returns the check result. It blocks briefly (up to 100ms)
// for the background check to complete. Returns nil if the check has not finished,
// the current version is not parseable, or an error occurred.
func (c *Checker) Result() *CheckResult {
	select {
	case <-c.done:
	case <-time.After(100 * time.Millisecond):
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *Checker) check(ctx context.Context) *CheckResult {
	cur, err := parseSemver(c.currentVersion)
	if err != nil {
		return nil
	}

	if cached, ok := c.readCache(); ok {
		latest, err := semver.NewVersion(cached)
		if err == nil {
			return &CheckResult{
				CurrentVersion: cur.String(),
				LatestVersion:  latest.String(),
				UpdateAvail:    latest.GreaterThan(cur),
			}
		}
	}

	latest, err := c.fetchLatestVersion(ctx)
	if err != nil {
		return nil
	}

	c.writeCache(latest.String())

	return &CheckResult{
		CurrentVersion: cur.String(),
		LatestVersion:  latest.String(),
		UpdateAvail:    latest.GreaterThan(cur),
	}
}

func (c *Checker) fetchLatestVersion(ctx context.Context) (*semver.Version, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases", c.apiBaseURL, c.owner, c.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "waza-version-check")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var releases []releaseInfo
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, err
	}

	for _, r := range releases {
		if strings.HasPrefix(r.TagName, "v") {
			v, err := parseSemver(r.TagName)
			if err == nil {
				return v, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid release found")
}

func (c *Checker) cachePath() string {
	if c.cacheDir == "" {
		return ""
	}
	return filepath.Join(c.cacheDir, cacheFile)
}

func (c *Checker) readCache() (string, bool) {
	path := c.cachePath()
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false
	}
	if time.Since(entry.CheckedAt) > cacheTTL {
		return "", false
	}
	return entry.LatestVersion, true
}

func (c *Checker) writeCache(version string) {
	path := c.cachePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
		return
	}
	entry := cacheEntry{
		LatestVersion: version,
		CheckedAt:     time.Now(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// PrintNotice prints an upgrade notice to stderr if a newer version is available.
// Returns true if a notice was printed.
func PrintNotice(result *CheckResult, installCmd string) bool {
	if result == nil || !result.UpdateAvail {
		return false
	}
	if installCmd == "" {
		installCmd = DefaultUpdateCommand
	}
	fmt.Fprintf(os.Stderr, "\nA newer version of waza is available: v%s \u2192 v%s. Run: %s\n",
		result.CurrentVersion, result.LatestVersion, installCmd)
	return true
}

func parseSemver(s string) (*semver.Version, error) {
	s = strings.TrimPrefix(s, "v")
	return semver.NewVersion(s)
}
