//go:build !windows

package graders

import (
	"io"
	"os"
)

// mkdirPrivateTemp creates a new directory with mode 0700.
func mkdirPrivateTemp(pattern string) (string, error) {
	return os.MkdirTemp("", pattern)
}

// createPrivateFile exclusively creates a file with mode 0600.
func createPrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// holdEntry keeps the entry open until cleanup so its inode cannot be reused
// by a replacement that would then pass the identity check.
func holdEntry(path string) (io.Closer, error) {
	d, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return d, nil
}
