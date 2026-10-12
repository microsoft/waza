package scaffold

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// ScenarioFiles returns a self-contained suite relative to its eval directory.
// It uses existing graders and the real agent executor, never a mock engine.
func ScenarioFiles(name, model, evalFile, taskGlob, taskSuffix, template string) (map[string]string, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	taskPattern := filepath.Clean(filepath.FromSlash(taskGlob))
	taskDir := filepath.Dir(taskPattern)
	taskPath := filepath.Join("tasks", "workflow"+taskSuffix)
	if !filepath.IsLocal(taskPath) || strings.Contains(taskSuffix, "/") || strings.Contains(taskSuffix, "\\") {
		return nil, fmt.Errorf("scenario task glob %q and suffix %q must select a local task file in a literal directory", taskGlob, taskSuffix)
	}
	matches, err := filepath.Match(taskPattern, taskPath)
	if err != nil {
		return nil, fmt.Errorf("parsing scenario task glob %q: %w", taskGlob, err)
	}
	if !matches {
		taskPath = filepath.Join(taskDir, "workflow"+taskSuffix)
		if !filepath.IsLocal(taskPath) || strings.ContainsAny(taskDir, "*?[:") {
			return nil, fmt.Errorf("scenario task glob %q cannot select the default task or a local task file in a literal directory", taskGlob)
		}
		matches, err = filepath.Match(taskPattern, taskPath)
		if err != nil {
			return nil, fmt.Errorf("parsing scenario task glob %q: %w", taskGlob, err)
		}
	}
	if !matches {
		return nil, fmt.Errorf("scenario task glob %q does not match generated task %q; use a glob matching workflow%s", taskGlob, taskPath, taskSuffix)
	}
	if filepath.Clean(evalFile) == taskPath {
		return nil, fmt.Errorf("scenario task path %q conflicts with the eval file", taskPath)
	}
	matchesEval, err := filepath.Match(taskPattern, filepath.Clean(evalFile))
	if err != nil {
		return nil, fmt.Errorf("matching eval file against scenario task glob %q: %w", taskGlob, err)
	}
	if matchesEval {
		return nil, fmt.Errorf("scenario task glob %q must not select the eval file %q", taskGlob, evalFile)
	}
	prompt := ""
	pattern := ""
	dependencies := ""
	fixture := false
	description := ""
	switch template {
	case "repository":
		description = "Real agent execution against sanitized repository fixtures."
		prompt = "Read inventory.txt. Write report.txt containing exactly: Inventory total: 3. Do not modify inventory.txt or create private.txt."
		pattern = `\AInventory total: 3(?:\r?\n)?\z`
		fixture = true
	case "cli":
		description = "Real agent execution using the installed git CLI, without command mocks."
		prompt = "Run git --version (read-only). Write its version line to report.txt. Do not initialize a repository, access network services, or create private.txt."
		pattern = "git version"
	case "mcp":
		description = "Real agent execution with a harness-only mocked MCP dependency; not production MCP reliability evidence."
		prompt = "Call the inventory MCP server list_items tool. Sum the returned quantities and write exactly Inventory total: 3 to report.txt. Do not create private.txt."
		pattern = `\AInventory total: 3(?:\r?\n)?\z`
		dependencies = `# Harness-only mocked dependency, not a production MCP server.
mcp_mocks:
  - name: inventory
    tools:
      list_items:
        description: Return sanitized inventory.
        input_schema:
          type: object
          properties: {}
        responses:
          - return:
              items:
                - name: apples
                  quantity: 2
                - name: pears
                  quantity: 1
`
	default:
		return nil, fmt.Errorf("unknown scenario template %q; choose repository, cli or mcp", template)
	}
	eval := fmt.Sprintf(`# Real agent executor. config.executor: mock checks the harness only.
name: %s
scenario: %s
description: %s
schemaVersion: %q
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: copilot-sdk
  model: %s
metrics:
  - name: task_completion
    weight: 1.0
    threshold: 1.0
graders:
  - type: file
    name: report-outcome
    config:
      must_exist: [report.txt]
      content_patterns:
        - path: report.txt
          must_match: [%s]
  - type: file
    name: private-file-boundary
    config:
      must_not_exist: [private.txt]
%stasks:
  - %q
`, strconv.Quote(name+"-eval"), strconv.Quote(name), strconv.Quote(description),
		models.ScenarioSchemaVersion, strconv.Quote(model), strconv.Quote(pattern), dependencies, taskGlob)
	task := fmt.Sprintf(`id: %s
name: Workflow outcome
description: %s
inputs:
  prompt: %s
`, strconv.Quote(name+"-001"), strconv.Quote(description), strconv.Quote(prompt))
	if fixture {
		task += "  context:\n    fixture: fixtures/inventory.txt\n"
	}
	files := map[string]string{
		evalFile:                   eval,
		filepath.ToSlash(taskPath): task,
		"fixtures/.gitkeep":        "",
	}
	if fixture {
		files["fixtures/inventory.txt"] = "apples: 2\npears: 1\n"
	}
	return files, nil
}
