package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindPath checks for a .waza.yaml entry without opening or parsing it.
// Like Load, it checks startDir and at most nine ancestors. Any existing entry,
// including a directory or dangling symlink, is returned. Absence is os.ErrNotExist.
func FindPath(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", startDir, err)
	}
	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, ".waza.yaml")
		if _, err := os.Lstat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("checking %q: %w", path, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", os.ErrNotExist
}
