// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T, releases []releaseInfo) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(releases)
		require.NoError(t, err)
	}))
}

func waitCheckResult(t *testing.T, ch *Checker) *CheckResult {
	t.Helper()
	select {
	case <-ch.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for version check")
	}
	return ch.Result()
}

func TestCheck_NewerVersionAvailable(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{{TagName: "v1.2.0"}, {TagName: "v1.1.0"}})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvail)
	assert.Equal(t, "1.0.0", result.CurrentVersion)
	assert.Equal(t, "1.2.0", result.LatestVersion)
}

func TestCheck_CurrentVersionIsCurrent(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{{TagName: "v1.0.0"}})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	assert.False(t, result.UpdateAvail)
}

func TestCheck_CurrentVersionIsNewer(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{{TagName: "v1.0.0"}})
	defer srv.Close()
	ch := NewChecker("v2.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	assert.False(t, result.UpdateAvail)
}

func TestCheck_DevVersion(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{{TagName: "v1.0.0"}})
	defer srv.Close()
	ch := NewChecker("dev", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	assert.Nil(t, ch.Result(), "dev version should be skipped")
}

func TestCheck_CacheHit(t *testing.T) {
	cacheDir := t.TempDir()
	entry := cacheEntry{LatestVersion: "2.0.0", CheckedAt: time.Now()}
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, cacheFile), data, 0o644))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("server should not be called when cache is valid")
	}))
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(cacheDir))
	ch.Run(context.Background())
	result := waitCheckResult(t, ch)
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvail)
	assert.Equal(t, "2.0.0", result.LatestVersion)
}

func TestCheck_CacheExpired(t *testing.T) {
	cacheDir := t.TempDir()
	entry := cacheEntry{LatestVersion: "1.5.0", CheckedAt: time.Now().Add(-25 * time.Hour)}
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, cacheFile), data, 0o644))
	srv := newTestServer(t, []releaseInfo{{TagName: "v2.0.0"}})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(cacheDir))
	ch.Run(context.Background())
	result := waitCheckResult(t, ch)
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvail)
	assert.Equal(t, "2.0.0", result.LatestVersion)
	cached, ok := ch.readCache()
	assert.True(t, ok)
	assert.Equal(t, "2.0.0", cached)
}

func TestCheck_APIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	assert.Nil(t, ch.Result(), "API failure should return nil")
}

func TestCheck_APITimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()),
		WithHTTPClient(&http.Client{Timeout: 100 * time.Millisecond}))
	ch.Run(context.Background())
	assert.Nil(t, ch.Result(), "timeout should return nil gracefully")
}

func TestCheck_SkipsAzdExtTags(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{
		{TagName: "azd-ext-microsoft-azd-waza_3.0.0"},
		{TagName: "v2.0.0"},
	})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvail)
	assert.Equal(t, "2.0.0", result.LatestVersion)
}

func TestCheck_NoReleases(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(context.Background())
	assert.Nil(t, ch.Result(), "no releases should return nil")
}

func TestCheck_CacheDirCreated(t *testing.T) {
	srv := newTestServer(t, []releaseInfo{{TagName: "v2.0.0"}})
	defer srv.Close()
	cacheDir := filepath.Join(t.TempDir(), "nested", "dir")
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(cacheDir))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	_, err := os.Stat(filepath.Join(cacheDir, cacheFile))
	assert.NoError(t, err)
}

func TestPrintNotice_NilResult(t *testing.T) {
	assert.False(t, PrintNotice(nil, ""))
}

func TestPrintNotice_NoUpdate(t *testing.T) {
	result := &CheckResult{CurrentVersion: "1.0.0", LatestVersion: "1.0.0", UpdateAvail: false}
	assert.False(t, PrintNotice(result, ""))
}

func TestPrintNotice_UpdateAvailable(t *testing.T) {
	result := &CheckResult{CurrentVersion: "1.0.0", LatestVersion: "2.0.0", UpdateAvail: true}
	r, w, _ := os.Pipe()
	oldStderr := os.Stderr
	os.Stderr = w
	printed := PrintNotice(result, "")
	_ = w.Close()
	os.Stderr = oldStderr
	out := make([]byte, 1024)
	n, _ := r.Read(out)
	assert.True(t, printed)
	output := string(out[:n])
	assert.Contains(t, output, "v1.0.0")
	assert.Contains(t, output, "v2.0.0")
	assert.Contains(t, output, "waza update")
}

func TestPrintNotice_CustomInstallCmd(t *testing.T) {
	result := &CheckResult{CurrentVersion: "1.0.0", LatestVersion: "2.0.0", UpdateAvail: true}
	r, w, _ := os.Pipe()
	oldStderr := os.Stderr
	os.Stderr = w
	printed := PrintNotice(result, "brew upgrade waza")
	_ = w.Close()
	os.Stderr = oldStderr
	out := make([]byte, 1024)
	n, _ := r.Read(out)
	assert.True(t, printed)
	assert.Contains(t, string(out[:n]), "brew upgrade waza")
}

func TestParseSemver(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"v1.2.3", "1.2.3", false},
		{"1.2.3", "1.2.3", false},
		{"v0.28.0", "0.28.0", false},
		{"dev", "", true},
		{"", "", true},
		{"not-a-version", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v, err := parseSemver(tt.input)
			if tt.err {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, v.String())
			}
		})
	}
}

func TestCheck_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(t.TempDir()))
	ch.Run(ctx)
	assert.Nil(t, ch.Result(), "canceled context should return nil")
}

func TestCheck_InvalidCacheJSON(t *testing.T) {
	cacheDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, cacheFile), []byte("not json"), 0o644))
	srv := newTestServer(t, []releaseInfo{{TagName: "v2.0.0"}})
	defer srv.Close()
	ch := NewChecker("v1.0.0", WithAPIBaseURL(srv.URL), WithCacheDir(cacheDir))
	ch.Run(context.Background())
	result := ch.Result()
	require.NotNil(t, result)
	assert.True(t, result.UpdateAvail)
	assert.Equal(t, "2.0.0", result.LatestVersion)
}
