package mcpmock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// PrepareSequenceState assigns attempt-owned state to cfg and creates one
// private directory for every finite response sequence.
func PrepareSequenceState(cfg *Config, root *os.Root, name string) error {
	if cfg == nil {
		return errors.New("mcp mock sequence state requires a configuration")
	}
	if root == nil || !filepath.IsAbs(root.Name()) || !filepath.IsLocal(name) || name == "." || filepath.Base(name) != name {
		return errors.New("mcp mock sequence state must be an absolute private path")
	}
	if err := root.Mkdir(name, 0700); err != nil {
		return fmt.Errorf("create mcp mock sequence state: %w", err)
	}
	cfg.StateDir = filepath.Join(root.Name(), name)
	for toolIndex, toolName := range sortedToolNames(cfg.Tools) {
		for responseIndex, response := range cfg.Tools[toolName].Responses {
			if len(response.Sequence) == 0 {
				continue
			}
			if err := root.Mkdir(sequenceStateDir(name, toolIndex, responseIndex), 0700); err != nil {
				return fmt.Errorf("create mcp mock sequence state for tool %q: %w", toolName, err)
			}
		}
	}
	return nil
}

func sortedToolNames(tools map[string]Tool) []string {
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sequenceStateDir(root string, toolIndex, responseIndex int) string {
	return filepath.Join(root, fmt.Sprintf("tool-%04d-response-%04d", toolIndex, responseIndex))
}
