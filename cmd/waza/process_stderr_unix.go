//go:build unix

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

type processStderr struct{}

func (processStderr) Write(data []byte) (int, error) {
	// os.File.Write on fd 2 turns EPIPE into process-wide SIGPIPE. A failed
	// diagnostic must instead preserve the command's selected exit status.
	n, err := unix.Write(syscall.Stderr, data)
	if n < 0 {
		n = 0
	}
	return n, err
}
