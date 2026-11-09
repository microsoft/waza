//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileRejectsLeafSymlinkReplacement(t *testing.T) {
	root, restore := raceDashboardRoot(t)
	defer restore()

	replacement := filepath.Join(t.TempDir(), "replacement.js")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	root.afterOpen = func(path string) {
		if path != "assets" {
			return
		}
		if err := os.Remove("web/dist/assets/app.js"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, "web/dist/assets/app.js"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := readFile(root, "assets/app.js"); err == nil || !strings.Contains(err.Error(), "nonsymlink") {
		t.Fatalf("leaf symlink replacement must fail closed: %v", err)
	}
}

func TestReadFileRetainsOpenedParentDuringReplacement(t *testing.T) {
	root, restore := raceDashboardRoot(t)
	defer restore()

	replacement := filepath.Join(t.TempDir(), "replacement-assets")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "app.js"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	root.afterOpen = func(path string) {
		if path != "assets" {
			return
		}
		if err := os.Rename("web/dist/assets", "web/dist/original-assets"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(replacement, "web/dist/assets"); err != nil {
			t.Fatal(err)
		}
	}

	content, err := readFile(root, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("parent replacement redirected the open: %q", content)
	}
}

func raceDashboardRoot(t *testing.T) (*dashboardRoot, func()) {
	t.Helper()
	oldWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web", "dist", "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "dist", "assets", "app.js"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	root, err := openDashboardRoot("web/dist")
	if err != nil {
		t.Fatal(err)
	}
	return root, func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
		if err := os.Chdir(oldWorkingDirectory); err != nil {
			t.Error(err)
		}
	}
}
