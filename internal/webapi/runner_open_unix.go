//go:build linux || darwin

package webapi

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRunnerArtifact(root *os.Root, name string) (*os.File, error) {
	// A regular directory entry can be replaced by a FIFO after ReadDir.
	// Nonblocking, no-follow admission keeps cancellation and Lab refresh safe.
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
