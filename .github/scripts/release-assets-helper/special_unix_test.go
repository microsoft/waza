//go:build darwin || linux

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNonregularFileDoesNotBlockCaller(t *testing.T) {
	root := fixture(t)
	pipe := filepath.Join(root, "web/dist/pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), hooks{}); err == nil {
		t.Fatal("FIFO admitted")
	}
}

func TestRegularFileReplacedWithFIFO(t *testing.T) {
	root := fixture(t)
	changed := false
	faults := hooks{beforeOpen: func(name string) error {
		if name != "index-abcdefgh.js" || changed {
			return nil
		}
		changed = true
		leaf := filepath.Join(root, "web/dist/assets", name)
		if err := os.Rename(leaf, leaf+"-old"); err != nil {
			return err
		}
		return syscall.Mkfifo(leaf, 0o600)
	}}
	_, err := caller(t, arguments("pack", root, filepath.Join(temp(t), "out"), "", testIdentity), faults)
	if !changed || err == nil || !strings.Contains(err.Error(), "changed during admission") {
		t.Fatalf("post-check FIFO did not fail promptly: changed=%v err=%v", changed, err)
	}
}

func TestSymlinkDestinationParentDoesNotEscape(t *testing.T) {
	artifact, digest := packed(t)
	parent, outside := temp(t), temp(t)
	link := filepath.Join(parent, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := caller(t, arguments("restore", artifact, filepath.Join(link, "dist"), digest, testIdentity), hooks{})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("symlink parent not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "dist")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("outside output created: %v", err)
	}
}
