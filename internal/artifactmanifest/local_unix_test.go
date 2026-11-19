//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package artifactmanifest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLocalRejectsSymlinksAndNonblockingSpecialFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(outside, []byte("outside bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := OpenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := local.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, kind := range []string{"symlink", "fifo"} {
		key := "blob-" + Hash([]byte(kind))
		path := filepath.Join(root, key)
		if kind == "symlink" {
			err = os.Symlink(outside, path)
		} else {
			err = unix.Mkfifo(path, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := local.Get(context.Background(), key, 100); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s opened or accepted: %v", kind, err)
		}
		if err := NewStore(local).Put(context.Background(), Artifact{SHA256: strings.TrimPrefix(key, "blob-"), SizeBytes: int64(len(kind))}, []byte(kind)); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s overwritten or accepted: %v", kind, err)
		}
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside bytes" {
		t.Fatalf("outside content was touched: %q %v", data, err)
	}
}
