//go:build windows

package rootedfile

import (
	"errors"
	"os"
)

func Open(root *os.Root, name string) (*os.File, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("assurance: input must be a regular file")
	}
	return root.Open(name)
}

// OpenPath preserves native path resolution, with the same best-effort precheck as Open.
func OpenPath(name string) (*os.File, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("assurance: input must be a regular file")
	}
	return os.Open(name)
}
