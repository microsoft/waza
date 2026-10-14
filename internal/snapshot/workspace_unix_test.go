//go:build linux || darwin

package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWorkspaceCaptureNoSymlinks(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "sub/a.txt", "safe")
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"linked.txt", filepath.Join(absolute, "sub/a.txt")},
		{"linked-dir", filepath.Join(absolute, "sub")},
		{"outside", ".."},
		{"root-link", absolute},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"linked.txt", "linked-dir/a.txt", "outside/anything"} {
		files, err := CaptureWorkspace(root, []string{name}, nil, nil, WorkspaceLimits{100, 200})
		if err == nil || len(files) != 0 {
			t.Fatalf("symlink accepted: %#v, %v", files, err)
		}
	}
	for _, suffix := range []string{"", "/", "/."} {
		files, err := CaptureWorkspace(filepath.Join(root, "root-link")+suffix, []string{"sub/a.txt"}, nil, nil, WorkspaceLimits{100, 200})
		if err == nil || len(files) != 0 {
			t.Fatalf("root symlink accepted: %#v, %v", files, err)
		}
	}
}

func TestWorkspaceCaptureFIFO(t *testing.T) {
	root := workspaceTestRoot(t)
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := CaptureWorkspace(root, []string{"pipe"}, nil, nil, WorkspaceLimits{100, 200})
	if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("FIFO must be rejected without blocking: %#v, %v", files, err)
	}
	files, err = CaptureWorkspace(root, []string{"pipe"}, []string{"pipe"}, nil, WorkspaceLimits{100, 200})
	if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "evaluator-only") {
		t.Fatalf("FIFO exclusion must precede open: %#v, %v", files, err)
	}
}
