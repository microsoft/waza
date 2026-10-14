package snapshot

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
)

var workspaceTestSequence atomic.Uint64

func workspaceTestRoot(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("safe workspace traversal is supported only on Linux and macOS")
	}
	root := ".workspace-test-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(workspaceTestSequence.Add(1), 10)
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	return root
}

func workspaceTestWrite(t *testing.T, root, name, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
