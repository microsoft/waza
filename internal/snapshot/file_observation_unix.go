//go:build linux || darwin

package snapshot

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func sameInventoryIdentity(metadata *unix.Stat_t, info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && metadata.Dev == stat.Dev && metadata.Ino == stat.Ino
}

func sameObservedFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) &&
		before.Size() == after.Size() && before.Mode() == after.Mode() &&
		before.ModTime().Equal(after.ModTime()) && sameChangeTime(before, after)
}
