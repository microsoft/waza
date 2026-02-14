package utils

import "path/filepath"

// ResolvePaths resolves a list of paths relative to a base directory.
// Absolute paths are returned unchanged, relative paths are resolved
// relative to the base directory.
func ResolvePaths(paths []string, baseDir string) []string {
	if len(paths) == 0 {
		return nil
	}

	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		if filepath.IsAbs(path) {
			resolved = append(resolved, path)
		} else {
			resolved = append(resolved, filepath.Join(baseDir, path))
		}
	}
	return resolved
}
