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

// IsFilteredPath reports whether path was explicitly configured but removed
// from filteredPaths. Paths are compared after resolving them against baseDir.
func IsFilteredPath(path string, configuredPaths, filteredPaths []string, baseDir string) bool {
	if path == "" {
		return false
	}
	target := filepath.Clean(ResolvePaths([]string{path}, baseDir)[0])
	allowed := make(map[string]bool, len(filteredPaths))
	for _, resolved := range ResolvePaths(filteredPaths, baseDir) {
		allowed[filepath.Clean(resolved)] = true
	}
	for _, resolved := range ResolvePaths(configuredPaths, baseDir) {
		if filepath.Clean(resolved) == target {
			return !allowed[target]
		}
	}
	return false
}
