package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// setupWorkspaceResources writes resource files into workspaceDir with path-traversal protection.
// Both CopilotEngine and MockEngine share this logic to keep sandbox behavior consistent.
func setupWorkspaceResources(workspaceDir string, resources []ResourceFile) error {
	baseWorkspace := filepath.Clean(workspaceDir)
	if baseWorkspace == "" {
		return fmt.Errorf("workspace is not set")
	}

	baseWithSep := baseWorkspace + string(os.PathSeparator)

	for _, res := range resources {
		if res.Path == "" {
			continue
		}

		relPath := filepath.Clean(res.Path)

		if filepath.IsAbs(relPath) {
			return fmt.Errorf("resource path %q must be relative", res.Path)
		}

		fullPath := filepath.Join(baseWorkspace, relPath)

		fullPathClean := filepath.Clean(fullPath)
		fullWithSep := fullPathClean + string(os.PathSeparator)

		if !strings.HasPrefix(fullWithSep, baseWithSep) {
			return fmt.Errorf("resource path %q escapes workspace", res.Path)
		}

		dir := filepath.Dir(fullPathClean)

		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating directory for resource %q: %w", res.Path, err)
		}

		if err := os.WriteFile(fullPathClean, res.Content, 0644); err != nil {
			return fmt.Errorf("writing resource %q: %w", res.Path, err)
		}
	}

	return nil
}
