package releasepolicy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func readArtifact(directory, name string) (data []byte, err error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid fixed release artifact name %q", name)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("release artifact %s must be a regular file, not a symlink or directory", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("release artifact %s changed while opening", name)
	}
	return io.ReadAll(file)
}
