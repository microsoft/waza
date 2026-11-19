//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package execution

import (
	"os"
	"syscall"
)

func openWorkspaceCaptureFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
