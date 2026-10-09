package testutil

import (
	"path/filepath"
	"runtime"
	"testing"
)

// RepoFile locates checked-in fixtures independently of a test package's cwd.
func RepoFile(t testing.TB, parts ...string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating testutil source")
	}
	return filepath.Join(append([]string{filepath.Dir(source), "..", ".."}, parts...)...)
}
