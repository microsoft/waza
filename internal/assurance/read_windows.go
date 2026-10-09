//go:build windows

package assurance

import (
	"errors"
	"os"
)

func openReferenceDocument(root *os.Root, name string) (*os.File, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("assurance: input must be a regular file")
	}
	return root.Open(name)
}
