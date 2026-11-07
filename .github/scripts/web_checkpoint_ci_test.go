package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func validateCheckpointDashboardSelection(source []byte) error {
	var workflow siteWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return err
	}
	steps := releaseSteps(workflow.Jobs["build"])
	filter, ok := steps["Detect dashboard changes"]
	if !ok || !strings.HasPrefix(filter.Uses, "dorny/paths-filter@") {
		return fmt.Errorf("checkpoint dashboard validation requires actual change detection")
	}
	var paths map[string][]string
	if err := yaml.Unmarshal([]byte(filter.With["filters"]), &paths); err != nil {
		return err
	}
	for name, patterns := range map[string][]string{
		"push":      workflow.On["push"].Paths,
		"PR filter": paths["web"],
	} {
		for _, control := range []struct {
			path     string
			selected bool
		}{
			{"internal/webapi/checkpoint_operations.go", true},
			{"internal/webapi/checkpoint_operations_test.go", true},
			{"internal/webapi/checkpoints.go", true},
			{"internal/webapi/checkpoint_operations.md", false},
			{"internal/webapi-near-miss/checkpoint_operations.go", false},
		} {
			selected := false
			for _, pattern := range patterns {
				matches, err := filepath.Match(pattern, control.path)
				if err != nil {
					return fmt.Errorf("%s pattern %q: %w", name, pattern, err)
				}
				selected = selected || matches
			}
			if selected != control.selected {
				return fmt.Errorf("%s selects %q=%t, want %t", name, control.path, selected, control.selected)
			}
		}
	}
	return nil
}

func TestCheckpointOperationsSelectDashboardValidation(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "web-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"checkpoint_operations.go", "checkpoint_operations_test.go"} {
		if _, err := os.Stat(filepath.Join("..", "..", "internal", "webapi", path)); err != nil {
			t.Fatalf("selection control must name the actual checkpoint caller: %v", err)
		}
	}
	if err := validateCheckpointDashboardSelection(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, line string
	}{
		{"missing-push-selection", "      - 'internal/webapi/checkpoint_operations*.go'\n"},
		{"missing-pr-selection", "              - 'internal/webapi/checkpoint_operations*.go'\n"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(string(source), mutation.line, "", 1)
			if changed == string(source) {
				t.Fatal("mutation must remove the production workflow's checkpoint selection")
			}
			if err := validateCheckpointDashboardSelection([]byte(changed)); err == nil {
				t.Fatal("checkpoint caller changes must not bypass dashboard validation")
			}
		})
	}
}
