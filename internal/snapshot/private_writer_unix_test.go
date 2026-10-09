//go:build linux || darwin

package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPrivateWriterPublication(t *testing.T) {
	base := workspaceTestRoot(t)
	root := filepath.Join(base, "private")
	published, err := writePrivateSnapshot(root, []byte(`{"content":"short"}`))
	if err != nil || filepath.Ext(published) != ".json" {
		t.Fatalf("private publication failed: %q, %v", published, err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("private directory must have mode 0700")
	}
	info, err = os.Stat(published)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("private file must have mode 0600")
	}
	content, err := os.ReadFile(published)
	if err != nil || string(content) != `{"content":"short"}` {
		t.Fatal("published content differs")
	}
	second, err := writePrivateSnapshot(root, []byte("different"))
	if err != nil || second == published {
		t.Fatal("publication must choose a new random JSON path")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatal("successful publication left temporary files")
	}
}

func TestPrivateWriterAdmissionReplacement(t *testing.T) {
	for _, replacement := range []string{"symlink", "permissive"} {
		t.Run(replacement, func(t *testing.T) {
			base := workspaceTestRoot(t)
			root := filepath.Join(base, "private")
			target := filepath.Join(base, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			published, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), privateWriterHooks{
				beforeAdmission: func() {
					if err := os.Rename(root, root+".old"); err != nil {
						t.Fatal(err)
					}
					if replacement == "symlink" {
						absolute, err := filepath.Abs(target)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(absolute, root); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(root, 0o755); err != nil {
						t.Fatal(err)
					}
				},
			})
			if err == nil || published != "" {
				t.Fatalf("unchecked replacement admitted: %q, %v", published, err)
			}
			for _, directory := range []string{root, root + ".old", target} {
				entries, readErr := os.ReadDir(directory)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("content entered an unchecked directory: %v", readErr)
				}
			}
		})
	}
}

func TestPrivateWriterPublicationReplacement(t *testing.T) {
	for _, replacement := range []string{"symlink", "private directory", "permissive directory", "chmod"} {
		t.Run(replacement, func(t *testing.T) {
			base := workspaceTestRoot(t)
			root := filepath.Join(base, "private")
			published, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), privateWriterHooks{
				beforePublication: func() {
					if replacement == "chmod" {
						if err := os.Chmod(root, 0o755); err != nil {
							t.Fatal(err)
						}
						return
					}
					if err := os.Rename(root, root+".old"); err != nil {
						t.Fatal(err)
					}
					if replacement == "symlink" {
						absolute, err := filepath.Abs(root + ".old")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(absolute, root); err != nil {
							t.Fatal(err)
						}
					} else {
						mode := os.FileMode(0o700)
						if replacement == "permissive directory" {
							mode = 0o755
						}
						if err := os.Mkdir(root, mode); err != nil {
							t.Fatal(err)
						}
					}
				},
			})
			if err == nil || published != "" || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("replaced caller path was returned: %q, %v", published, err)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("replacement contains published or incomplete evidence")
			}
			if replacement != "chmod" {
				entries, readErr = os.ReadDir(root + ".old")
				if readErr != nil || len(entries) != 0 {
					t.Fatal("admitted descriptor cleanup failed")
				}
			}
		})
	}
}

func TestPrivateWriterDoesNotOverwrite(t *testing.T) {
	for _, existing := range []string{"file", "symlink"} {
		t.Run(existing, func(t *testing.T) {
			base := workspaceTestRoot(t)
			root := filepath.Join(base, "private")
			var final string
			published, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), privateWriterHooks{
				beforePublication: func() {
					entries, err := os.ReadDir(root)
					if err != nil || len(entries) != 1 {
						t.Fatal("missing exclusive temporary file")
					}
					name := strings.TrimSuffix(strings.TrimPrefix(entries[0].Name(), "."), ".tmp")
					final = filepath.Join(root, name)
					if existing == "file" {
						if err := os.WriteFile(final, []byte("existing"), 0o600); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Symlink("missing", final); err != nil {
							t.Fatal(err)
						}
					}
				},
			})
			if err == nil || published != "" {
				t.Fatalf("existing entry was overwritten: %q, %v", published, err)
			}
			if existing == "file" {
				content, readErr := os.ReadFile(final)
				if readErr != nil || string(content) != "existing" {
					t.Fatal("existing content changed")
				}
			} else if target, readErr := os.Readlink(final); readErr != nil || target != "missing" {
				t.Fatal("existing symlink changed")
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 1 {
				t.Fatal("failed publication left incomplete evidence")
			}
		})
	}
}

func TestPrivateWriterReplacementAfterLink(t *testing.T) {
	base := workspaceTestRoot(t)
	root := filepath.Join(base, "private")
	published, err := writePrivateSnapshotWithHooks(root, []byte("sensitive"), privateWriterHooks{
		afterPublication: func() {
			if err := os.Rename(root, root+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil || published != "" || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("replacement after link must not return a misleading path: %q, %v", published, err)
	}
	for _, directory := range []string{root, root + ".old"} {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil || len(entries) != 0 {
			t.Fatal("publication rollback left sensitive evidence")
		}
	}
}

func TestPrivateWriterConcurrent(t *testing.T) {
	base := workspaceTestRoot(t)
	root := filepath.Join(base, "private")
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			published, err := writePrivateSnapshot(root, []byte("safe"))
			if err != nil || published == "" {
				t.Errorf("concurrent publication failed: %q, %v", published, err)
			}
		})
	}
	group.Wait()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 8 {
		t.Fatal("concurrent publication lost evidence or left incomplete files")
	}
}

func TestPrivateWriterInvalidRoots(t *testing.T) {
	base := workspaceTestRoot(t)
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", file} {
		if published, err := writePrivateSnapshot(root, []byte("sensitive")); err == nil || published != "" {
			t.Fatalf("invalid root accepted: %q, %v", published, err)
		}
	}
}
