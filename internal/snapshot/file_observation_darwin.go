//go:build darwin

package snapshot

import (
	"os"
	"syscall"
)

func sameChangeTime(before, after os.FileInfo) bool {
	b, bok := before.Sys().(*syscall.Stat_t)
	a, aok := after.Sys().(*syscall.Stat_t)
	return bok && aok && b.Ctimespec == a.Ctimespec
}
