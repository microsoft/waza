//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package assurance

import (
	"os"

	"golang.org/x/sys/unix"
)

func openReferenceDocument(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
