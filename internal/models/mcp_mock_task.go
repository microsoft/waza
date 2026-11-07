package models

import (
	"fmt"
	"strings"
)

// ValidateMCPMocksSchemaVersion checks that the enclosing eval admits MCP mocks,
// whether they are declared in the eval or in one of its tasks.
func ValidateMCPMocksSchemaVersion(spec *EvalSpec) error {
	_, minor, err := parseSchemaVersion(spec.SchemaVersion)
	if err != nil {
		return err
	}
	if minor < 1 && spec.Scenario == "" {
		return fmt.Errorf("mcp_mocks requires schemaVersion 1.1 or newer")
	}
	return nil
}

// ValidateMCPMockNames requires nonempty mock names that are unique after trimming.
func ValidateMCPMockNames(mocks []MCPMockConfig) error {
	seen := make(map[string]bool, len(mocks))
	for i, mock := range mocks {
		name := strings.TrimSpace(mock.Name)
		if name == "" {
			return fmt.Errorf("mcp_mocks[%d].name is required", i)
		}
		if seen[name] {
			return fmt.Errorf("mcp_mocks[%d].name %q is duplicated", i, name)
		}
		seen[name] = true
	}
	return nil
}

// EffectiveMCPMocks returns the MCP mocks one task runs with. A task mock
// replaces the eval mock with the same name in place and other eval mocks stay;
// a task mock with a new name is appended. The eval list is never modified.
func EffectiveMCPMocks(spec *EvalSpec, tc *TestCase) []MCPMockConfig {
	var evalMocks []MCPMockConfig
	if spec != nil {
		evalMocks = spec.MCPMocks
	}
	if tc == nil || len(tc.MCPMocks) == 0 {
		return evalMocks
	}
	effective := make([]MCPMockConfig, 0, len(evalMocks)+len(tc.MCPMocks))
	effective = append(effective, evalMocks...)
	index := make(map[string]int, len(effective))
	for i, mock := range effective {
		index[strings.TrimSpace(mock.Name)] = i
	}
	for _, mock := range tc.MCPMocks {
		name := strings.TrimSpace(mock.Name)
		if i, ok := index[name]; ok {
			effective[i] = mock
			continue
		}
		index[name] = len(effective)
		effective = append(effective, mock)
	}
	return effective
}

// MCPMockReplacedByEveryTask reports whether every task in taskMocks declares
// a task mock with the given name. Such an eval mock is never started. It is
// false when there are no tasks.
func MCPMockReplacedByEveryTask(name string, taskMocks [][]MCPMockConfig) bool {
	if len(taskMocks) == 0 {
		return false
	}
	name = strings.TrimSpace(name)
	for _, mocks := range taskMocks {
		replaced := false
		for _, mock := range mocks {
			if strings.TrimSpace(mock.Name) == name {
				replaced = true
				break
			}
		}
		if !replaced {
			return false
		}
	}
	return true
}

// TaskMCPMocks returns each task's declared mcp_mocks, for
// MCPMockReplacedByEveryTask.
func TaskMCPMocks(tasks []*TestCase) [][]MCPMockConfig {
	taskMocks := make([][]MCPMockConfig, 0, len(tasks))
	for _, task := range tasks {
		if task != nil {
			taskMocks = append(taskMocks, task.MCPMocks)
		}
	}
	return taskMocks
}
