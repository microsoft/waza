//go:build linux || darwin

package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func materializeTestSnapshot() *Snapshot {
	return &Snapshot{WorkspaceFiles: []WorkspaceFile{
		{Path: "sub/nested/output.txt", Content: "exact UTF-8 日本語\n"},
		{Path: "unused.txt", Content: "not requested"},
	}}
}

func materializeTestOwnRoot(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error("remove owned materialization test directory failed")
		}
	})
}

func TestMaterializePrivateRequiredRestoration(t *testing.T) {
	workspace, err := materializeVerifiedWorkspace(materializeTestSnapshot(), []string{"sub/nested/output.txt"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Path()
	materializeTestOwnRoot(t, root)
	for _, name := range []string{"", "sub", "sub/nested"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatal("restoration directories are not private")
		}
	}
	file := filepath.Join(root, "sub/nested/output.txt")
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatal("restored file is not a private regular file")
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != materializeTestSnapshot().WorkspaceFiles[0].Content {
		t.Fatal("restored content is not exact")
	}
	if _, err := os.Stat(filepath.Join(root, "unused.txt")); !os.IsNotExist(err) {
		t.Fatal("an unrequested file was restored")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) || workspace.Path() != "" {
		t.Fatal("close did not remove the private restoration")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal("close must be idempotent")
	}
}

func TestMaterializeNoFollowAndExclusiveFiles(t *testing.T) {
	for _, attack := range []string{"symlink directory", "permissive directory", "existing file", "symlink file"} {
		t.Run(attack, func(t *testing.T) {
			outside := workspaceTestRoot(t)
			workspaceTestWrite(t, outside, "unchanged.txt", "original")
			absolute, err := filepath.Abs(outside)
			if err != nil {
				t.Fatal(err)
			}
			name := "sub/output.txt"
			if strings.Contains(attack, "file") {
				name = "output.txt"
			}
			snap := &Snapshot{WorkspaceFiles: []WorkspaceFile{{Path: name, Content: "must not escape"}}}
			workspace, err := materializeWithHooks(snap, []string{name}, materializeHooks{
				afterAdmission: func(root string) { materializeTestOwnRoot(t, root) },
				beforeFile: func(root, _ string) {
					switch attack {
					case "symlink directory":
						if err := os.Symlink(absolute, filepath.Join(root, "sub")); err != nil {
							t.Fatal(err)
						}
					case "permissive directory":
						if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(filepath.Join(root, "sub"), 0o755); err != nil {
							t.Fatal(err)
						}
					case "existing file":
						if err := os.WriteFile(filepath.Join(root, name), []byte("existing"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "symlink file":
						if err := os.Symlink(filepath.Join(absolute, "unchanged.txt"), filepath.Join(root, name)); err != nil {
							t.Fatal(err)
						}
					}
				},
			})
			if err == nil || workspace != nil || strings.Contains(err.Error(), absolute) || strings.Contains(err.Error(), "must not escape") {
				t.Fatalf("unsafe restoration was admitted or diagnostic leaked: %v", err)
			}
			content, readErr := os.ReadFile(filepath.Join(outside, "unchanged.txt"))
			if readErr != nil || string(content) != "original" {
				t.Fatal("restoration changed an outside file")
			}
			if _, err := os.Stat(filepath.Join(outside, "output.txt")); !os.IsNotExist(err) {
				t.Fatal("restoration escaped through a directory symlink")
			}
		})
	}
}

func TestMaterializePublicationIdentity(t *testing.T) {
	for _, attack := range []string{"root directory", "root symlink", "root permissions", "child symlink", "file symlink"} {
		t.Run(attack, func(t *testing.T) {
			workspace, err := materializeWithHooks(materializeTestSnapshot(), []string{"sub/nested/output.txt"}, materializeHooks{
				afterAdmission: func(root string) { materializeTestOwnRoot(t, root) },
				beforePublication: func(root string) {
					switch attack {
					case "root directory", "root symlink":
						materializeTestOwnRoot(t, root+".old")
						if err := os.Rename(root, root+".old"); err != nil {
							t.Fatal(err)
						}
						if attack == "root directory" {
							if err := os.Mkdir(root, 0o700); err != nil {
								t.Fatal(err)
							}
						} else if err := os.Symlink(root+".old", root); err != nil {
							t.Fatal(err)
						}
					case "root permissions":
						if err := os.Chmod(root, 0o755); err != nil {
							t.Fatal(err)
						}
					case "child symlink":
						if err := os.Rename(filepath.Join(root, "sub"), filepath.Join(root, "old")); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink("old", filepath.Join(root, "sub")); err != nil {
							t.Fatal(err)
						}
					case "file symlink":
						file := filepath.Join(root, "sub/nested/output.txt")
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink("missing", file); err != nil {
							t.Fatal(err)
						}
					}
				},
			})
			if err == nil || workspace != nil || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("misleading restoration path was returned: %v", err)
			}
		})
	}
}

func TestMaterializeCleanupFailureIsPropagated(t *testing.T) {
	workspace, err := materializeVerifiedWorkspace(materializeTestSnapshot(), []string{"sub/nested/output.txt"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Path()
	materializeTestOwnRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "unexpected"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("cleanup failure was lost or leaked the root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove restored private entries")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal("descriptors must only be closed once after cleanup failure")
	}
}

func TestMaterializeVerificationPrecedesRestore(t *testing.T) {
	if workspace, err := MaterializeWorkspace(nil, models.EvidenceOrigin{}, "", []string{"output.txt"}); err == nil || workspace != nil {
		t.Fatal("unverified workspace materialized")
	}
}

func TestMaterializeConcurrent(t *testing.T) {
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			workspace, err := materializeVerifiedWorkspace(materializeTestSnapshot(), []string{"sub/nested/output.txt"})
			if err != nil {
				t.Error(err)
				return
			}
			if err := workspace.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
}
