package releasepolicy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/assurance"
)

func readArtifact(directory, name string) (data []byte, err error) {
	return readArtifactContext(context.Background(), directory, name)
}

func readArtifactContext(ctx context.Context, directory, name string) (data []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	data, err = assurance.ReadDocument(ctx, root, name, 16<<20)
	if err != nil {
		return nil, err
	}
	confirmed, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !confirmed.Mode().IsRegular() || !os.SameFile(info, confirmed) {
		return nil, fmt.Errorf("release artifact %s changed while reading", name)
	}
	return data, nil
}
